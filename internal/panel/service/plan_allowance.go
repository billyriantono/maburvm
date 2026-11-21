package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// AllowanceError refuses something a VM's plan does not include. Its message
// is written for the person who asked.
type AllowanceError struct{ Message string }

func (e *AllowanceError) Error() string { return e.Message }

// vmPlan is the plan a VM is on, nil when it is on none. A plan deleted since
// still governs the VMs sold on it.
func vmPlan(ctx context.Context, db *gorm.DB, vm *models.VM) (*models.Plan, error) {
	if vm.PlanID == nil || *vm.PlanID == "" {
		return nil, nil
	}
	var plan models.Plan
	if err := db.WithContext(ctx).Unscoped().First(&plan, "id = ?", *vm.PlanID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the VM's plan: %w", err)
	}
	return &plan, nil
}

// allowanceCheck refuses one more when a VM already has max of something.
// noun is singular; nouns pluralises it.
func allowanceCheck(max *int, have int64, noun, nouns, remedy string) error {
	if max == nil || have < int64(*max) {
		return nil
	}
	if *max == 0 {
		return &AllowanceError{Message: fmt.Sprintf("This VM's plan does not include %s.", nouns)}
	}
	n := nouns
	if *max == 1 {
		n = noun
	}
	return &AllowanceError{Message: fmt.Sprintf("This VM's plan includes %d %s and it has %d. %s", *max, n, have, remedy)}
}

// checkVMAllowance applies the plan's allowance (picked by pick) to a VM that
// has `have` already. A VM on no plan has no allowance to keep to.
func checkVMAllowance(ctx context.Context, db *gorm.DB, vm *models.VM, have int64, pick func(*models.Plan) *int, noun, nouns, remedy string) error {
	plan, err := vmPlan(ctx, db, vm)
	if err != nil || plan == nil {
		return err
	}
	return allowanceCheck(pick(plan), have, noun, nouns, remedy)
}

// keptBackups counts the backups a VM holds against its allowance: all but
// the failed ones.
func keptBackups(ctx context.Context, db *gorm.DB, vmID string) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Model(&models.Backup{}).
		Where("vm_id = ? AND status <> ?", vmID, models.BackupStatusFailed).Count(&n).Error
	return n, err
}

// accountAllowance adds up an account-wide allowance from the plans its VMs
// are on: each VM brings its plan's figure. It is nil, no limit, when the
// account has no VMs or any of them is on no plan or on a plan that sets no
// limit, so existing accounts and plans keep working as before.
func accountAllowance(ctx context.Context, db *gorm.DB, accountID string, pick func(*models.Plan) *int) (*int, error) {
	var planIDs []sql.NullString
	if err := db.WithContext(ctx).Table("vms").Where("user_id = ? AND deleted_at IS NULL", accountID).
		Pluck("plan_id", &planIDs).Error; err != nil {
		return nil, fmt.Errorf("failed to read the account's plans: %w", err)
	}
	if len(planIDs) == 0 {
		return nil, nil
	}
	plans := map[string]*models.Plan{}
	total := 0
	for _, id := range planIDs {
		if !id.Valid || id.String == "" {
			return nil, nil
		}
		p, ok := plans[id.String]
		if !ok {
			var plan models.Plan
			if err := db.WithContext(ctx).Unscoped().First(&plan, "id = ?", id.String).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, nil
				}
				return nil, err
			}
			p, plans[id.String] = &plan, &plan
		}
		v := pick(p)
		if v == nil {
			return nil, nil
		}
		total += *v
	}
	return &total, nil
}

// checkAccountAllowance refuses one more of something the account already
// holds `have` of, against what its plans add up to.
func checkAccountAllowance(ctx context.Context, db *gorm.DB, accountID string, have int64, pick func(*models.Plan) *int, noun, nouns string) error {
	max, err := accountAllowance(ctx, db, accountID, pick)
	if err != nil || max == nil || have < int64(*max) {
		return err
	}
	if *max == 0 {
		return &AllowanceError{Message: fmt.Sprintf("The plans of this account's servers do not include %s.", nouns)}
	}
	n := nouns
	if *max == 1 {
		n = noun
	}
	return &AllowanceError{Message: fmt.Sprintf("The plans of this account's servers include %d %s and it has %d. Delete one to make another, or move a server to a plan with more.", *max, n, have)}
}

func planMaxImages(p *models.Plan) *int      { return p.MaxImages }
func planMaxFloatingIPs(p *models.Plan) *int { return p.MaxFloatingIPs }
func planMaxExtraDisks(p *models.Plan) *int  { return p.MaxExtraDisks }

// checkImageAllowance applies the images allowance to a customer capturing
// an image of vm; staff images are theirs and outside it.
func (s *ImageService) checkImageAllowance(ctx context.Context, vm *models.VM, isAdmin bool) error {
	if isAdmin {
		return nil
	}
	var have int64
	if err := s.db.WithContext(ctx).Model(&models.Image{}).
		Where("user_id = ? AND status <> ?", vm.UserID, models.ImageStatusFailed).Count(&have).Error; err != nil {
		return err
	}
	return checkAccountAllowance(ctx, s.db, vm.UserID, have, planMaxImages, "image", "images")
}

// checkExtraDiskAllowance applies the plan's extra-disk allowance to vm.
func checkExtraDiskAllowance(ctx context.Context, db *gorm.DB, vm *models.VM) error {
	var have int64
	if err := db.WithContext(ctx).Model(&models.VMDisk{}).Where("vm_id = ?", vm.ID).Count(&have).Error; err != nil {
		return err
	}
	return checkVMAllowance(ctx, db, vm, have, planMaxExtraDisks, "extra disk", "extra disks", "Detach one to add another.")
}

// checkFloatingIPAllowance applies the floating-IP allowance to an account
// that already holds `held`.
func checkFloatingIPAllowance(ctx context.Context, db *gorm.DB, userID string, held int64) error {
	return checkAccountAllowance(ctx, db, userID, held, planMaxFloatingIPs, "floating IP", "floating IPs")
}
