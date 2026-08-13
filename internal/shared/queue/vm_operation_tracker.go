package queue

import (
	"context"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// isDomainNotFound reports whether an agent error/message means the libvirt
// domain doesn't exist, so a delete against it has effectively already happened.
func isDomainNotFound(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "domain not found") ||
		strings.Contains(m, "no domain with matching")
}

// startVMOperation inserts a running VMOperation row and returns its ID. Best
// effort: on error it returns "" so callers never fail the real work just because
// progress couldn't be recorded.
func startVMOperation(ctx context.Context, db *gorm.DB, vmID, operation, firstLabel string, totalSteps int) string {
	if db == nil {
		return ""
	}
	op := &models.VMOperation{
		VMID:        vmID,
		Operation:   operation,
		Status:      models.VMOperationRunning,
		CurrentStep: 1,
		TotalSteps:  totalSteps,
		StepLabel:   firstLabel,
	}
	if err := db.WithContext(ctx).Create(op).Error; err != nil {
		return ""
	}
	return op.ID
}

// stepVMOperation advances a running operation to the given step + label.
func stepVMOperation(ctx context.Context, db *gorm.DB, opID string, step int, label string) {
	if db == nil || opID == "" {
		return
	}
	_ = db.WithContext(ctx).Model(&models.VMOperation{}).Where("id = ?", opID).Updates(map[string]interface{}{
		"current_step": step,
		"step_label":   label,
		"updated_at":   time.Now(),
	}).Error
}

// completeVMOperation marks an operation finished successfully.
func completeVMOperation(ctx context.Context, db *gorm.DB, opID, label string) {
	if db == nil || opID == "" {
		return
	}
	now := time.Now()
	// Advance the step counter to the end as well. Without it a finished
	// operation kept whatever step it last announced, so the dialog read
	// "VM deleted · step 2/3" beside a full progress bar — three claims, one of
	// which says the work is not finished. A completed operation is at its last
	// step by definition.
	_ = db.WithContext(ctx).Model(&models.VMOperation{}).Where("id = ?", opID).Updates(map[string]interface{}{
		"status":       models.VMOperationCompleted,
		"current_step": gorm.Expr("total_steps"),
		"step_label":   label,
		"updated_at":   now,
		"completed_at": now,
	}).Error
}

// failVMOperation marks an operation failed with an error message.
func failVMOperation(ctx context.Context, db *gorm.DB, opID, errMsg string) {
	if db == nil || opID == "" {
		return
	}
	now := time.Now()
	_ = db.WithContext(ctx).Model(&models.VMOperation{}).Where("id = ?", opID).Updates(map[string]interface{}{
		"status":       models.VMOperationFailed,
		"error":        errMsg,
		"updated_at":   now,
		"completed_at": now,
	}).Error
}
