package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// PortForwardRepository handles database operations for port forwards
type PortForwardRepository struct {
	db *gorm.DB
}

// NewPortForwardRepository creates a new PortForwardRepository
func NewPortForwardRepository(db *gorm.DB) *PortForwardRepository {
	return &PortForwardRepository{db: db}
}

// Create creates a new port forward
func (r *PortForwardRepository) Create(ctx context.Context, pf *models.PortForward) error {
	// Check for port conflicts on same network
	if err := r.checkPortConflict(ctx, pf); err != nil {
		return err
	}

	return r.db.WithContext(ctx).Create(pf).Error
}

// GetByID retrieves a port forward by ID
func (r *PortForwardRepository) GetByID(ctx context.Context, id string) (*models.PortForward, error) {
	var pf models.PortForward
	if err := r.db.WithContext(ctx).First(&pf, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &pf, nil
}

// GetByVM retrieves all port forwards for a VM
func (r *PortForwardRepository) GetByVM(ctx context.Context, vmID string) ([]models.PortForward, error) {
	var pfs []models.PortForward
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Find(&pfs).Error; err != nil {
		return nil, err
	}
	return pfs, nil
}

// GetByNetwork retrieves all port forwards on a network
func (r *PortForwardRepository) GetByNetwork(ctx context.Context, networkID string) ([]models.PortForward, error) {
	var pfs []models.PortForward
	if err := r.db.WithContext(ctx).Where("network_id = ?", networkID).Find(&pfs).Error; err != nil {
		return nil, err
	}
	return pfs, nil
}

// GetByPort retrieves a port forward by external port on a network
func (r *PortForwardRepository) GetByPort(ctx context.Context, networkID string, externalPort int) (*models.PortForward, error) {
	var pf models.PortForward
	if err := r.db.WithContext(ctx).
		Where("network_id = ? AND external_port = ?", networkID, externalPort).
		First(&pf).Error; err != nil {
		return nil, err
	}
	return &pf, nil
}

// CheckPortConflict checks if a port is already in use on a network
func (r *PortForwardRepository) CheckPortConflict(ctx context.Context, networkID string, externalPort int, excludeID string) (bool, error) {
	query := r.db.WithContext(ctx).
		Where("network_id = ? AND external_port = ?", networkID, externalPort)

	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	var count int64
	if err := query.Model(&models.PortForward{}).Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

// checkPortConflict checks for port conflicts before creating/updating
func (r *PortForwardRepository) checkPortConflict(ctx context.Context, pf *models.PortForward) error {
	conflict, err := r.CheckPortConflict(ctx, pf.NetworkID, pf.ExternalPort, pf.ID)
	if err != nil {
		return err
	}
	if conflict {
		return fmt.Errorf("port %d is already in use on network %s", pf.ExternalPort, pf.NetworkID)
	}
	return nil
}

// Update updates a port forward
func (r *PortForwardRepository) Update(ctx context.Context, pf *models.PortForward) error {
	// Check for port conflicts
	if err := r.checkPortConflict(ctx, pf); err != nil {
		return err
	}

	pf.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).Save(pf).Error
}

// Delete deletes a port forward
func (r *PortForwardRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.PortForward{}, "id = ?", id).Error
}

// DeleteByVM deletes all port forwards for a VM
func (r *PortForwardRepository) DeleteByVM(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Delete(&models.PortForward{}, "vm_id = ?", vmID).Error
}

// List retrieves all port forwards with pagination
func (r *PortForwardRepository) List(ctx context.Context, page, pageSize int) ([]models.PortForward, error) {
	var pfs []models.PortForward
	offset := (page - 1) * pageSize
	if err := r.db.WithContext(ctx).Offset(offset).Limit(pageSize).Find(&pfs).Error; err != nil {
		return nil, err
	}
	return pfs, nil
}
