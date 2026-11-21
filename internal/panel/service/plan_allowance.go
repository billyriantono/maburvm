package service

import (
	"context"
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
