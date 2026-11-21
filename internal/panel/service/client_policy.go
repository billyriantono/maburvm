package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Rules for what a customer (the client role) may do on their own, as opposed
// to what an operator or the billing system does for them.

var (
	// ErrVMSuspended is returned when a customer tries to change, or make
	// something out of, a VM the provider has suspended.
	ErrVMSuspended = errors.New("this VM is suspended; contact support to restore service")
	// ErrPlanRequired is returned when a customer order names no plan.
	ErrPlanRequired = errors.New("choose a plan: servers are ordered by plan, not by a custom size")
	// ErrPlanInactive is returned when an order names a plan that is no longer sold.
	ErrPlanInactive = errors.New("plan is not active")
	// ErrSelfServiceDisabled is returned for a customer action the operator has
	// switched off in the system settings.
	ErrSelfServiceDisabled = errors.New("not available for self-service")
)

// OwnerAccess is the one rule for what a VM's owner may do with it: nothing to
// a VM that is not theirs, and nothing but read it or delete it while it is
// suspended. change is false for reads and for deleting the VM itself; a
// clone, an image, a disk, a backup or a restore are all changes. Admins pass.
func OwnerAccess(vm *models.VM, userID string, isAdmin, change bool) error {
	if isAdmin {
		return nil
	}
	if vm.UserID != userID {
		return ErrVMNotFound // a stranger's VM and no VM look the same
	}
	if change && (vm.Suspended || vm.Status == models.VMStatusSuspended) {
		return ErrVMSuspended
	}
	return nil
}

func checkOwnerAccess(ctx context.Context, vmRepo *repository.VMRepository, vmID, userID string, isAdmin, change bool) error {
	if isAdmin {
		return nil
	}
	vm, err := vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	return OwnerAccess(vm, userID, false, change)
}

// CheckOwnerAccess applies OwnerAccess to a VM by id.
func (s *VMService) CheckOwnerAccess(ctx context.Context, vmID, userID string, isAdmin, change bool) error {
	return checkOwnerAccess(ctx, s.vmRepo, vmID, userID, isAdmin, change)
}

// CheckOwnerAccess applies OwnerAccess to a VM by id.
func (s *BackupService) CheckOwnerAccess(ctx context.Context, vmID, userID string, isAdmin, change bool) error {
	return checkOwnerAccess(ctx, s.vmRepo, vmID, userID, isAdmin, change)
}

// SelfServiceAction is something a customer can do without the operator or the
// billing system, and that the operator can switch off.
type SelfServiceAction string

const (
	SelfServiceOrder     SelfServiceAction = "order"
	SelfServiceClone     SelfServiceAction = "clone"
	SelfServiceExtraDisk SelfServiceAction = "extra_disk"
)

// SelfServiceAllowed reports whether customers may do this themselves. Unset
// means allowed, which is how the panel behaved before the switches existed.
func SelfServiceAllowed(ctx context.Context, db *gorm.DB, action SelfServiceAction) bool {
	s := loadQuotaSettings(ctx, db)
	var v *bool
	switch action {
	case SelfServiceOrder:
		v = s.ClientOrderingEnabled
	case SelfServiceClone:
		v = s.ClientCloneEnabled
	case SelfServiceExtraDisk:
		v = s.ClientExtraDisksEnabled
	}
	return v == nil || *v
}

// SelfServiceAllowed reads the switch from this service's database.
func (s *VMService) SelfServiceAllowed(ctx context.Context, action SelfServiceAction) bool {
	return SelfServiceAllowed(ctx, s.db, action)
}

// SelfServiceSwitches is every switch at once, for a page that shows them.
func SelfServiceSwitches(ctx context.Context, db *gorm.DB) map[SelfServiceAction]bool {
	out := map[SelfServiceAction]bool{}
	for _, a := range []SelfServiceAction{SelfServiceOrder, SelfServiceClone, SelfServiceExtraDisk} {
		out[a] = SelfServiceAllowed(ctx, db, a)
	}
	return out
}

// NewClientQuota is the quota a new customer account starts with. It is finite
// unless the operator sets a limit to 0, which keeps "0 = no limit".
func NewClientQuota(ctx context.Context, db *gorm.DB) SetQuotaRequest {
	s := loadQuotaSettings(ctx, db)
	pick := func(v *int, def int) int {
		if v != nil && *v >= 0 {
			return *v
		}
		return def
	}
	return SetQuotaRequest{
		MaxVMs:    pick(s.NewClientMaxVMs, DefaultNewClientMaxVMs),
		MaxVCPU:   pick(s.NewClientMaxVCPU, DefaultNewClientMaxVCPU),
		MaxRAMMB:  pick(s.NewClientMaxRAMMB, DefaultNewClientMaxRAMMB),
		MaxDiskGB: pick(s.NewClientMaxDiskGB, DefaultNewClientMaxDiskGB),
	}
}

// ApplyNewClientQuota gives a just-created customer account its starting quota.
// Call it in the transaction that creates the account: an account left without
// a quota row is unlimited.
func ApplyNewClientQuota(ctx context.Context, tx *gorm.DB, userID string) error {
	q := NewClientQuota(ctx, tx)
	return repository.NewQuotaRepository(tx).Upsert(ctx, &models.UserQuota{
		UserID: userID, MaxVMs: q.MaxVMs, MaxVCPU: q.MaxVCPU, MaxRAMMB: q.MaxRAMMB, MaxDiskGB: q.MaxDiskGB,
	})
}
