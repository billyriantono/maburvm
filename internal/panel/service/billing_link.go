package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// A VM's link to what was sold: the plan it is on and the billing service
// that pays for it.

// ErrBillingManaged is returned when a customer tries to delete a VM their
// billing service pays for: the service would go on being charged.
var ErrBillingManaged = errors.New("this server is managed by your billing account; cancel the service there to remove it")

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// writeBillingLink stores the plan and billing reference of a VM. By table
// name: the fields are read-only on the model, and an update through the model
// is silently dropped for a read-only field.
func writeBillingLink(tx *gorm.DB, vm *models.VM, planID, billingRef *string) error {
	if err := tx.Table("vms").Where("id = ?", vm.ID).
		Updates(map[string]interface{}{"plan_id": planID, "billing_ref": billingRef}).Error; err != nil {
		return fmt.Errorf("failed to record the VM's plan and billing reference: %w", err)
	}
	vm.PlanID, vm.BillingRef = planID, billingRef
	return nil
}

// GetVMRecord returns the VM row alone, with no node round trip.
func (s *VMService) GetVMRecord(ctx context.Context, vmID string) (*models.VM, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	return vm, nil
}

// GetPlan returns a plan by id, active or not.
func (s *VMService) GetPlan(ctx context.Context, planID string) (*models.Plan, error) {
	plan, err := s.planRepo.GetByID(ctx, planID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPlanNotFound
		}
		return nil, fmt.Errorf("failed to get plan: %w", err)
	}
	return plan, nil
}

// SetBillingLink changes a VM's plan or billing reference. A nil argument
// leaves that one as it is; a pointer to "" clears it. It records what the VM
// is sold as and changes nothing on the VM itself.
func (s *VMService) SetBillingLink(ctx context.Context, vmID string, planID, billingRef *string) (*models.VM, error) {
	vm, err := s.GetVMRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	newPlan, newRef := vm.PlanID, vm.BillingRef
	if planID != nil {
		// A plan the VM is already on is not looked up again: it may have been
		// deleted since, and that must not block a change of the reference.
		if *planID != "" && (vm.PlanID == nil || *vm.PlanID != *planID) {
			if _, err := s.GetPlan(ctx, *planID); err != nil {
				return nil, err
			}
		}
		newPlan = strPtrOrNil(*planID)
	}
	if billingRef != nil {
		if len(*billingRef) > 255 {
			return nil, fmt.Errorf("%w: billing reference is longer than 255 characters", ErrInvalidResources)
		}
		newRef = strPtrOrNil(*billingRef)
	}
	if err := writeBillingLink(s.db.WithContext(ctx), vm, newPlan, newRef); err != nil {
		return nil, err
	}
	return vm, nil
}

// ClientMayDeleteBilledVMs reports whether customers may delete a VM that a
// billing service pays for. Unset means no: the service would keep charging
// for a VM that is gone.
func ClientMayDeleteBilledVMs(ctx context.Context, db *gorm.DB) bool {
	v := loadQuotaSettings(ctx, db).ClientDeleteBilledEnabled
	return v != nil && *v
}

// OwnerMayDelete is the rule for a customer deleting their own VM: any VM of
// theirs, suspended or not, except one a billing service pays for while the
// operator has not allowed that.
func (s *VMService) OwnerMayDelete(ctx context.Context, vm *models.VM, userID string) error {
	if err := CheckVMAccess(ctx, s.db, vm, userID, false, AccessOperate); err != nil {
		return err
	}
	if vm.BillingManaged() && !ClientMayDeleteBilledVMs(ctx, s.db) {
		return ErrBillingManaged
	}
	return nil
}

// DeleteVMByOwner deletes a VM on its owner's request.
func (s *VMService) DeleteVMByOwner(ctx context.Context, vmID, userID string) error {
	vm, err := s.GetVMRecord(ctx, vmID)
	if err != nil {
		return err
	}
	if err := s.OwnerMayDelete(ctx, vm, userID); err != nil {
		return err
	}
	return s.DeleteVM(ctx, vmID)
}

// ApplyPlanLimits puts a plan's speed cap, transfer quota and over-quota
// policy on a VM's primary interface, and sends the new speed to the node
// through the same network-config job a manual speed change uses. A VM with
// no interface has nothing to limit.
func (s *NetworkService) ApplyPlanLimits(ctx context.Context, vmID string, plan *models.Plan) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}
	network, err := s.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("failed to get the VM's network: %w", err)
	}
	policy := plan.OverQuotaPolicy
	if policy == "" {
		policy = models.OverQuotaThrottle
	}
	if err := s.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", network.ID).
		Updates(map[string]interface{}{
			"bandwidth_limit":     int64(plan.BandwidthMbps),
			"over_quota_policy":   policy,
			"throttle_speed_mbps": plan.ThrottleSpeedMbps,
		}).Error; err != nil {
		return fmt.Errorf("failed to store the plan's speed cap: %w", err)
	}
	// Also resets this period's allowance, and lifts the over-quota mark when
	// the new allowance covers what was used.
	if err := repository.NewBandwidthUsageRepository(s.db).SetQuota(ctx, vmID, plan.DataQuotaGB); err != nil {
		return fmt.Errorf("failed to store the plan's transfer quota: %w", err)
	}
	// A VM throttled for going over keeps its throttle here; the collector
	// restores it to the new speed once it is back under its allowance.
	if network.Throttled {
		return nil
	}
	network.BandwidthLimit = int64(plan.BandwidthMbps)
	if err := s.enqueueNetworkConfigJob(ctx, vm, network); err != nil {
		return fmt.Errorf("the plan's limits are saved, but the new speed could not be sent to the node: %w", err)
	}
	return nil
}
