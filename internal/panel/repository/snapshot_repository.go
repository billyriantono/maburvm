package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// SnapshotRepository provides data access for VM snapshots
type SnapshotRepository struct {
	base *BaseRepository[models.Snapshot]
	db   *gorm.DB
}

// NewSnapshotRepository creates a new SnapshotRepository instance
func NewSnapshotRepository(db *gorm.DB) *SnapshotRepository {
	return &SnapshotRepository{
		base: NewBaseRepository[models.Snapshot](db),
		db:   db,
	}
}

// GetByID retrieves a snapshot by ID
func (r *SnapshotRepository) GetByID(ctx context.Context, id string) (*models.Snapshot, error) {
	return r.base.GetByID(ctx, id)
}

// GetByIDWithVM retrieves a snapshot by ID with VM eagerly loaded
func (r *SnapshotRepository) GetByIDWithVM(ctx context.Context, id string) (*models.Snapshot, error) {
	var snapshot models.Snapshot
	if err := r.db.WithContext(ctx).Preload("VM").First(&snapshot, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// List retrieves all snapshots with optional pagination
func (r *SnapshotRepository) List(ctx context.Context, limit, offset int) ([]models.Snapshot, error) {
	return r.base.List(ctx, limit, offset)
}

// ListByVMID retrieves snapshots filtered by VM ID with optional pagination
func (r *SnapshotRepository) ListByVMID(ctx context.Context, vmID string, limit, offset int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot
	query := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// ListByStatus retrieves snapshots filtered by status with optional pagination
func (r *SnapshotRepository) ListByStatus(ctx context.Context, status models.SnapshotStatus, limit, offset int) ([]models.Snapshot, error) {
	var snapshots []models.Snapshot
	query := r.db.WithContext(ctx).Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&snapshots).Error; err != nil {
		return nil, err
	}
	return snapshots, nil
}

// Create inserts a new snapshot
func (r *SnapshotRepository) Create(ctx context.Context, snapshot *models.Snapshot) error {
	return r.base.Create(ctx, snapshot)
}

// Update updates an existing snapshot
func (r *SnapshotRepository) Update(ctx context.Context, snapshot *models.Snapshot) error {
	return r.base.Update(ctx, snapshot)
}

// Delete removes a snapshot by ID (hard delete)
func (r *SnapshotRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of snapshots
func (r *SnapshotRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByVMID returns the number of snapshots for a VM
func (r *SnapshotRepository) CountByVMID(ctx context.Context, vmID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByStatus returns the number of snapshots with a specific status
func (r *SnapshotRepository) CountByStatus(ctx context.Context, status models.SnapshotStatus) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("status = ?", status).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateStatus updates a snapshot's status
func (r *SnapshotRepository) UpdateStatus(ctx context.Context, id string, status models.SnapshotStatus) error {
	return r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateDiskPath updates a snapshot's disk path
func (r *SnapshotRepository) UpdateDiskPath(ctx context.Context, id string, diskPath string) error {
	return r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("id = ?", id).Update("disk_path", diskPath).Error
}

// GetByVMIDAndName retrieves a snapshot by VM ID and name
func (r *SnapshotRepository) GetByVMIDAndName(ctx context.Context, vmID string, name string) (*models.Snapshot, error) {
	var snapshot models.Snapshot
	if err := r.db.WithContext(ctx).Where("vm_id = ? AND name = ?", vmID, name).First(&snapshot).Error; err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// NameExists checks if a snapshot name already exists for a VM
func (r *SnapshotRepository) NameExists(ctx context.Context, vmID string, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ? AND name = ?", vmID, name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetIDsByVMID returns all snapshot IDs for a VM
func (r *SnapshotRepository) GetIDsByVMID(ctx context.Context, vmID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vmID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetLatestByVMID retrieves the most recent snapshot for a VM
func (r *SnapshotRepository) GetLatestByVMID(ctx context.Context, vmID string) (*models.Snapshot, error) {
	var snapshot models.Snapshot
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC").First(&snapshot).Error; err != nil {
		return nil, err
	}
	return &snapshot, nil
}
