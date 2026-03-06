package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// BackupScheduleRepository provides data access for backup schedules
type BackupScheduleRepository struct {
	base *BaseRepository[models.BackupSchedule]
	db   *gorm.DB
}

// NewBackupScheduleRepository creates a new BackupScheduleRepository instance
func NewBackupScheduleRepository(db *gorm.DB) *BackupScheduleRepository {
	return &BackupScheduleRepository{
		base: NewBaseRepository[models.BackupSchedule](db),
		db:   db,
	}
}

// GetByID retrieves a backup schedule by ID
func (r *BackupScheduleRepository) GetByID(ctx context.Context, id string) (*models.BackupSchedule, error) {
	return r.base.GetByID(ctx, id)
}

// GetByVMID retrieves a backup schedule by VM ID
func (r *BackupScheduleRepository) GetByVMID(ctx context.Context, vmID string) (*models.BackupSchedule, error) {
	var schedule models.BackupSchedule
	if err := r.db.WithContext(ctx).Preload("VM").First(&schedule, "vm_id = ?", vmID).Error; err != nil {
		return nil, err
	}
	return &schedule, nil
}

// GetByIDWithVM retrieves a backup schedule by ID with VM eagerly loaded
func (r *BackupScheduleRepository) GetByIDWithVM(ctx context.Context, id string) (*models.BackupSchedule, error) {
	var schedule models.BackupSchedule
	if err := r.db.WithContext(ctx).Preload("VM").First(&schedule, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &schedule, nil
}

// List retrieves all backup schedules with optional pagination
func (r *BackupScheduleRepository) List(ctx context.Context, limit, offset int) ([]models.BackupSchedule, error) {
	var schedules []models.BackupSchedule
	query := r.db.WithContext(ctx).Preload("VM")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&schedules).Error; err != nil {
		return nil, err
	}
	return schedules, nil
}

// ListByStatus retrieves backup schedules filtered by status
func (r *BackupScheduleRepository) ListByStatus(ctx context.Context, status models.BackupScheduleStatus, limit, offset int) ([]models.BackupSchedule, error) {
	var schedules []models.BackupSchedule
	query := r.db.WithContext(ctx).Preload("VM").Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&schedules).Error; err != nil {
		return nil, err
	}
	return schedules, nil
}

// ListActive retrieves all active backup schedules
func (r *BackupScheduleRepository) ListActive(ctx context.Context) ([]models.BackupSchedule, error) {
	var schedules []models.BackupSchedule
	if err := r.db.WithContext(ctx).Preload("VM").Where("status = ?", models.BackupScheduleStatusActive).Find(&schedules).Error; err != nil {
		return nil, err
	}
	return schedules, nil
}

// Create inserts a new backup schedule
func (r *BackupScheduleRepository) Create(ctx context.Context, schedule *models.BackupSchedule) error {
	return r.base.Create(ctx, schedule)
}

// Update updates an existing backup schedule
func (r *BackupScheduleRepository) Update(ctx context.Context, schedule *models.BackupSchedule) error {
	return r.base.Update(ctx, schedule)
}

// Delete removes a backup schedule by ID (hard delete)
func (r *BackupScheduleRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// DeleteByVMID removes a backup schedule by VM ID
func (r *BackupScheduleRepository) DeleteByVMID(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Unscoped().Delete(&models.BackupSchedule{}, "vm_id = ?", vmID).Error
}

// Count returns the total number of backup schedules
func (r *BackupScheduleRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByStatus returns the number of backup schedules with a specific status
func (r *BackupScheduleRepository) CountByStatus(ctx context.Context, status models.BackupScheduleStatus) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("status = ?", status).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateStatus updates a backup schedule's status
func (r *BackupScheduleRepository) UpdateStatus(ctx context.Context, id string, status models.BackupScheduleStatus) error {
	return r.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateNextRun updates the next scheduled run time
func (r *BackupScheduleRepository) UpdateNextRun(ctx context.Context, id string, nextRunAt interface{}) error {
	return r.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("id = ?", id).Update("next_run_at", nextRunAt).Error
}

// UpdateLastRun updates the last run time and last backup ID
func (r *BackupScheduleRepository) UpdateLastRun(ctx context.Context, id string, lastRunAt interface{}, lastBackupID *string) error {
	updates := map[string]interface{}{
		"last_run_at": lastRunAt,
	}
	if lastBackupID != nil {
		updates["last_backup_id"] = *lastBackupID
	}
	return r.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("id = ?", id).Updates(updates).Error
}

// ExistsByVMID checks if a schedule exists for a VM
func (r *BackupScheduleRepository) ExistsByVMID(ctx context.Context, vmID string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
