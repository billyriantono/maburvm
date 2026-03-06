package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// BackupRepository provides data access for backups
type BackupRepository struct {
	base *BaseRepository[models.Backup]
	db   *gorm.DB
}

// NewBackupRepository creates a new BackupRepository instance
func NewBackupRepository(db *gorm.DB) *BackupRepository {
	return &BackupRepository{
		base: NewBaseRepository[models.Backup](db),
		db:   db,
	}
}

// GetByID retrieves a backup by ID
func (r *BackupRepository) GetByID(ctx context.Context, id string) (*models.Backup, error) {
	return r.base.GetByID(ctx, id)
}

// GetByIDWithVM retrieves a backup by ID with VM eagerly loaded
func (r *BackupRepository) GetByIDWithVM(ctx context.Context, id string) (*models.Backup, error) {
	var backup models.Backup
	if err := r.db.WithContext(ctx).Preload("VM").First(&backup, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &backup, nil
}

// List retrieves all backups with optional pagination
func (r *BackupRepository) List(ctx context.Context, limit, offset int) ([]models.Backup, error) {
	return r.base.List(ctx, limit, offset)
}

// ListByVMID retrieves backups filtered by VM ID with optional pagination
func (r *BackupRepository) ListByVMID(ctx context.Context, vmID string, limit, offset int) ([]models.Backup, error) {
	var backups []models.Backup
	query := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// ListByStatus retrieves backups filtered by status with optional pagination
func (r *BackupRepository) ListByStatus(ctx context.Context, status models.BackupStatus, limit, offset int) ([]models.Backup, error) {
	var backups []models.Backup
	query := r.db.WithContext(ctx).Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// ListByStorageProvider retrieves backups filtered by storage provider
func (r *BackupRepository) ListByStorageProvider(ctx context.Context, provider string, limit, offset int) ([]models.Backup, error) {
	var backups []models.Backup
	query := r.db.WithContext(ctx).Where("storage_provider = ?", provider)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&backups).Error; err != nil {
		return nil, err
	}
	return backups, nil
}

// Create inserts a new backup
func (r *BackupRepository) Create(ctx context.Context, backup *models.Backup) error {
	return r.base.Create(ctx, backup)
}

// Update updates an existing backup
func (r *BackupRepository) Update(ctx context.Context, backup *models.Backup) error {
	return r.base.Update(ctx, backup)
}

// Delete removes a backup by ID (hard delete)
func (r *BackupRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of backups
func (r *BackupRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByVMID returns the number of backups for a VM
func (r *BackupRepository) CountByVMID(ctx context.Context, vmID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Backup{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByStatus returns the number of backups with a specific status
func (r *BackupRepository) CountByStatus(ctx context.Context, status models.BackupStatus) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Backup{}).Where("status = ?", status).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateStatus updates a backup's status
func (r *BackupRepository) UpdateStatus(ctx context.Context, id string, status models.BackupStatus) error {
	return r.db.WithContext(ctx).Model(&models.Backup{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateSize updates a backup's size
func (r *BackupRepository) UpdateSize(ctx context.Context, id string, size int64) error {
	return r.db.WithContext(ctx).Model(&models.Backup{}).Where("id = ?", id).Update("size", size).Error
}

// GetTotalSizeByVMID returns the total size of all backups for a VM
func (r *BackupRepository) GetTotalSizeByVMID(ctx context.Context, vmID string) (int64, error) {
	var totalSize int64
	if err := r.db.WithContext(ctx).Model(&models.Backup{}).Where("vm_id = ?", vmID).Select("COALESCE(SUM(size), 0)").Scan(&totalSize).Error; err != nil {
		return 0, err
	}
	return totalSize, nil
}
