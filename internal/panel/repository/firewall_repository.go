package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// FirewallRepository provides data access for firewall rules
type FirewallRepository struct {
	base *BaseRepository[models.FirewallRule]
	db   *gorm.DB
}

// NewFirewallRepository creates a new FirewallRepository instance
func NewFirewallRepository(db *gorm.DB) *FirewallRepository {
	return &FirewallRepository{
		base: NewBaseRepository[models.FirewallRule](db),
		db:   db,
	}
}

// GetByID retrieves a firewall rule by ID
func (r *FirewallRepository) GetByID(ctx context.Context, id string) (*models.FirewallRule, error) {
	return r.base.GetByID(ctx, id)
}

// GetByIDWithVM retrieves a firewall rule by ID with VM eagerly loaded
func (r *FirewallRepository) GetByIDWithVM(ctx context.Context, id string) (*models.FirewallRule, error) {
	var rule models.FirewallRule
	if err := r.db.WithContext(ctx).Preload("VM").First(&rule, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// List retrieves all firewall rules with optional pagination
func (r *FirewallRepository) List(ctx context.Context, limit, offset int) ([]models.FirewallRule, error) {
	return r.base.List(ctx, limit, offset)
}

// ListWithVM retrieves all firewall rules with VM eagerly loaded
func (r *FirewallRepository) ListWithVM(ctx context.Context, limit, offset int) ([]models.FirewallRule, error) {
	var rules []models.FirewallRule
	query := r.db.WithContext(ctx).Preload("VM")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// ListByVMID retrieves firewall rules filtered by VM ID with optional pagination
func (r *FirewallRepository) ListByVMID(ctx context.Context, vmID string, limit, offset int) ([]models.FirewallRule, error) {
	var rules []models.FirewallRule
	query := r.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("priority ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// ListByVMIDAndDirection retrieves firewall rules filtered by VM ID and direction
func (r *FirewallRepository) ListByVMIDAndDirection(ctx context.Context, vmID string, direction string, limit, offset int) ([]models.FirewallRule, error) {
	var rules []models.FirewallRule
	query := r.db.WithContext(ctx).Where("vm_id = ? AND direction = ?", vmID, direction).Order("priority ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// ListByProtocol retrieves firewall rules filtered by protocol
func (r *FirewallRepository) ListByProtocol(ctx context.Context, protocol string, limit, offset int) ([]models.FirewallRule, error) {
	var rules []models.FirewallRule
	query := r.db.WithContext(ctx).Where("protocol = ?", protocol)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// ListByAction retrieves firewall rules filtered by action (allow/deny)
func (r *FirewallRepository) ListByAction(ctx context.Context, action string, limit, offset int) ([]models.FirewallRule, error) {
	var rules []models.FirewallRule
	query := r.db.WithContext(ctx).Where("action = ?", action)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// Create inserts a new firewall rule
func (r *FirewallRepository) Create(ctx context.Context, rule *models.FirewallRule) error {
	return r.base.Create(ctx, rule)
}

// Update updates an existing firewall rule
func (r *FirewallRepository) Update(ctx context.Context, rule *models.FirewallRule) error {
	return r.base.Update(ctx, rule)
}

// Delete removes a firewall rule by ID (hard delete as per PRD compliance requirements)
func (r *FirewallRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// DeleteByVMID removes all firewall rules for a specific VM
func (r *FirewallRepository) DeleteByVMID(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Unscoped().Where("vm_id = ?", vmID).Delete(&models.FirewallRule{}).Error
}

// Count returns the total number of firewall rules
func (r *FirewallRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByVMID returns the number of firewall rules for a specific VM
func (r *FirewallRepository) CountByVMID(ctx context.Context, vmID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("vm_id = ?", vmID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdatePriority updates a firewall rule's priority
func (r *FirewallRepository) UpdatePriority(ctx context.Context, id string, priority int) error {
	return r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("id = ?", id).Update("priority", priority).Error
}

// UpdateAction updates a firewall rule's action (allow/deny)
func (r *FirewallRepository) UpdateAction(ctx context.Context, id string, action string) error {
	return r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("id = ?", id).Update("action", action).Error
}

// UpdateSourceIP updates a firewall rule's source IP
func (r *FirewallRepository) UpdateSourceIP(ctx context.Context, id string, sourceIP string) error {
	return r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("id = ?", id).Update("source_ip", sourceIP).Error
}

// UpdatePortRange updates a firewall rule's port range
func (r *FirewallRepository) UpdatePortRange(ctx context.Context, id string, portRange string) error {
	return r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("id = ?", id).Update("port_range", portRange).Error
}

// GetIDs returns all firewall rule IDs
func (r *FirewallRepository) GetIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.FirewallRule{}).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByVMID returns all firewall rule IDs for a specific VM
func (r *FirewallRepository) GetIDsByVMID(ctx context.Context, vmID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.FirewallRule{}).Where("vm_id = ?", vmID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
