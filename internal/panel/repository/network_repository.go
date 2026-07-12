package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// NetworkRepository provides data access for network configurations
type NetworkRepository struct {
	base *BaseRepository[models.Network]
	db   *gorm.DB
}

// NewNetworkRepository creates a new NetworkRepository instance
func NewNetworkRepository(db *gorm.DB) *NetworkRepository {
	return &NetworkRepository{
		base: NewBaseRepository[models.Network](db),
		db:   db,
	}
}

// WithDB returns a NetworkRepository bound to the supplied database handle/transaction.
func (r *NetworkRepository) WithDB(db *gorm.DB) *NetworkRepository {
	return NewNetworkRepository(db)
}

// GetByID retrieves a network configuration by ID
func (r *NetworkRepository) GetByID(ctx context.Context, id string) (*models.Network, error) {
	return r.base.GetByID(ctx, id)
}

// GetByIDWithVM retrieves a network configuration by ID with VM eagerly loaded
func (r *NetworkRepository) GetByIDWithVM(ctx context.Context, id string) (*models.Network, error) {
	var network models.Network
	if err := r.db.WithContext(ctx).Preload("VM").First(&network, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &network, nil
}

// GetByVMID retrieves the network configuration for a specific VM
func (r *NetworkRepository) GetByVMID(ctx context.Context, vmID string) (*models.Network, error) {
	var network models.Network
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&network).Error; err != nil {
		return nil, err
	}
	return &network, nil
}

// GetByIPAddress retrieves a network configuration by IP address
func (r *NetworkRepository) GetByIPAddress(ctx context.Context, ipAddress string) (*models.Network, error) {
	var network models.Network
	if err := r.db.WithContext(ctx).Where("ip_address = ?", ipAddress).First(&network).Error; err != nil {
		return nil, err
	}
	return &network, nil
}

// List retrieves all network configurations with optional pagination
func (r *NetworkRepository) List(ctx context.Context, limit, offset int) ([]models.Network, error) {
	return r.base.List(ctx, limit, offset)
}

// ListWithVM retrieves all network configurations with VM eagerly loaded
func (r *NetworkRepository) ListWithVM(ctx context.Context, limit, offset int) ([]models.Network, error) {
	var networks []models.Network
	query := r.db.WithContext(ctx).Preload("VM")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&networks).Error; err != nil {
		return nil, err
	}
	return networks, nil
}

// ListByVMID retrieves all network configurations for a specific VM
func (r *NetworkRepository) ListByVMID(ctx context.Context, vmID string) ([]models.Network, error) {
	var networks []models.Network
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Find(&networks).Error; err != nil {
		return nil, err
	}
	return networks, nil
}

// ListByVLANID retrieves network configurations filtered by VLAN ID
func (r *NetworkRepository) ListByVLANID(ctx context.Context, vlanID int, limit, offset int) ([]models.Network, error) {
	var networks []models.Network
	query := r.db.WithContext(ctx).Where("vlan_id = ?", vlanID)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&networks).Error; err != nil {
		return nil, err
	}
	return networks, nil
}

// Create inserts a new network configuration
func (r *NetworkRepository) Create(ctx context.Context, network *models.Network) error {
	return r.base.Create(ctx, network)
}

// Update updates an existing network configuration
func (r *NetworkRepository) Update(ctx context.Context, network *models.Network) error {
	return r.base.Update(ctx, network)
}

// Delete removes a network configuration by ID (hard delete as per PRD compliance requirements)
func (r *NetworkRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// DeleteByVMID removes all network configurations for a specific VM
func (r *NetworkRepository) DeleteByVMID(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Unscoped().Where("vm_id = ?", vmID).Delete(&models.Network{}).Error
}

// Count returns the total number of network configurations
func (r *NetworkRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByVMID returns the number of network configurations for a specific VM
func (r *NetworkRepository) CountByVMID(ctx context.Context, vmID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Network{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateBandwidthLimit updates a network's bandwidth limit
func (r *NetworkRepository) UpdateBandwidthLimit(ctx context.Context, id string, limit int64) error {
	return r.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", id).Update("bandwidth_limit", limit).Error
}

// SetThrottled flags a network as (un)throttled by quota enforcement.
func (r *NetworkRepository) SetThrottled(ctx context.Context, id string, throttled bool) error {
	return r.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", id).Update("throttled", throttled).Error
}

// ListThrottled returns all networks currently throttled by quota enforcement,
// used to restore their normal speed once the quota resets.
func (r *NetworkRepository) ListThrottled(ctx context.Context) ([]models.Network, error) {
	var nets []models.Network
	err := r.db.WithContext(ctx).Where("throttled = ?", true).Find(&nets).Error
	return nets, err
}

// UpdateVLANID updates a network's VLAN ID
func (r *NetworkRepository) UpdateVLANID(ctx context.Context, id string, vlanID *int) error {
	return r.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", id).Update("vlan_id", vlanID).Error
}

// UpdateIPAddress updates a network's IP address
func (r *NetworkRepository) UpdateIPAddress(ctx context.Context, id string, ipAddress string) error {
	return r.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", id).Update("ip_address", ipAddress).Error
}

// IPAddressExists checks if an IP address is already in use
func (r *NetworkRepository) IPAddressExists(ctx context.Context, ipAddress string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Network{}).Where("ip_address = ?", ipAddress).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetIDs returns all network configuration IDs
func (r *NetworkRepository) GetIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.Network{}).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByVMID returns all network configuration IDs for a specific VM
func (r *NetworkRepository) GetIDsByVMID(ctx context.Context, vmID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.Network{}).Where("vm_id = ?", vmID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// UpdateAntiSpoofing updates a network's anti-spoofing flag
func (r *NetworkRepository) UpdateAntiSpoofing(ctx context.Context, id string, enabled bool) error {
	return r.db.WithContext(ctx).Model(&models.Network{}).Where("id = ?", id).Update("anti_spoofing", enabled).Error
}
