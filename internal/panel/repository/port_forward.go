package repository

import (
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// PortForwardRepository provides data access for port forwarding rules
type PortForwardRepository struct {
	db *gorm.DB
}

// NewPortForwardRepository creates a new PortForwardRepository instance
func NewPortForwardRepository(db *gorm.DB) *PortForwardRepository {
	return &PortForwardRepository{db: db}
}

// Create inserts a new port forward rule
func (r *PortForwardRepository) Create(pf *models.PortForward) error {
	return r.db.Create(pf).Error
}

// GetByID retrieves a port forward by ID
func (r *PortForwardRepository) GetByID(id string) (*models.PortForward, error) {
	var pf models.PortForward
	if err := r.db.First(&pf, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &pf, nil
}

// GetByVMID retrieves all port forwards for a VM
func (r *PortForwardRepository) GetByVMID(vmID string) ([]models.PortForward, error) {
	var forwards []models.PortForward
	if err := r.db.Where("vm_id = ?", vmID).Find(&forwards).Error; err != nil {
		return nil, err
	}
	return forwards, nil
}

// GetByNetworkID retrieves all port forwards for a network
func (r *PortForwardRepository) GetByNetworkID(networkID string) ([]models.PortForward, error) {
	var forwards []models.PortForward
	if err := r.db.Where("network_id = ?", networkID).Find(&forwards).Error; err != nil {
		return nil, err
	}
	return forwards, nil
}

// GetByExternalPort retrieves a port forward by external port
func (r *PortForwardRepository) GetByExternalPort(externalPort int, protocol string) (*models.PortForward, error) {
	var pf models.PortForward
	if err := r.db.Where("external_port = ? AND protocol = ?", externalPort, protocol).First(&pf).Error; err != nil {
		return nil, err
	}
	return &pf, nil
}

// Update updates an existing port forward
func (r *PortForwardRepository) Update(pf *models.PortForward) error {
	return r.db.Save(pf).Error
}

// Delete removes a port forward by ID
func (r *PortForwardRepository) Delete(id string) error {
	return r.db.Delete(&models.PortForward{}, "id = ?", id).Error
}

// DeleteByVMID removes all port forwards for a VM
func (r *PortForwardRepository) DeleteByVMID(vmID string) error {
	return r.db.Delete(&models.PortForward{}, "vm_id = ?", vmID).Error
}

// DeleteByNetworkID removes all port forwards for a network
func (r *PortForwardRepository) DeleteByNetworkID(networkID string) error {
	return r.db.Delete(&models.PortForward{}, "network_id = ?", networkID).Error
}

// List retrieves all port forwards with pagination
func (r *PortForwardRepository) List(limit, offset int) ([]models.PortForward, error) {
	var forwards []models.PortForward
	query := r.db
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&forwards).Error; err != nil {
		return nil, err
	}
	return forwards, nil
}

// Count returns the total number of port forwards
func (r *PortForwardRepository) Count() (int64, error) {
	var count int64
	if err := r.db.Model(&models.PortForward{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByVMID returns the number of port forwards for a VM
func (r *PortForwardRepository) CountByVMID(vmID string) (int64, error) {
	var count int64
	if err := r.db.Model(&models.PortForward{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// ExternalPortExists checks if an external port is already in use
func (r *PortForwardRepository) ExternalPortExists(externalPort int, protocol string) (bool, error) {
	var count int64
	if err := r.db.Model(&models.PortForward{}).Where("external_port = ? AND protocol = ?", externalPort, protocol).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
