package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// NodeRepository provides data access for compute nodes
type NodeRepository struct {
	base *BaseRepository[models.Node]
	db   *gorm.DB
}

// NewNodeRepository creates a new NodeRepository instance
func NewNodeRepository(db *gorm.DB) *NodeRepository {
	return &NodeRepository{
		base: NewBaseRepository[models.Node](db),
		db:   db,
	}
}

// GetByID retrieves a node by ID
func (r *NodeRepository) GetByID(ctx context.Context, id string) (*models.Node, error) {
	return r.base.GetByID(ctx, id)
}

// GetByToken retrieves a node by its authentication token
func (r *NodeRepository) GetByToken(ctx context.Context, token string) (*models.Node, error) {
	var node models.Node
	if err := r.db.WithContext(ctx).Where("token = ?", token).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// GetByIPAddress retrieves a node by its IP address
func (r *NodeRepository) GetByIPAddress(ctx context.Context, ipAddress string) (*models.Node, error) {
	var node models.Node
	if err := r.db.WithContext(ctx).Where("ip_address = ?", ipAddress).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// List retrieves all nodes with optional pagination
func (r *NodeRepository) List(ctx context.Context, limit, offset int) ([]models.Node, error) {
	return r.base.List(ctx, limit, offset)
}

// ListByStatus retrieves nodes filtered by status with optional pagination
func (r *NodeRepository) ListByStatus(ctx context.Context, status models.NodeStatus, limit, offset int) ([]models.Node, error) {
	var nodes []models.Node
	query := r.db.WithContext(ctx).Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// ListActive retrieves all active nodes
func (r *NodeRepository) ListActive(ctx context.Context) ([]models.Node, error) {
	var nodes []models.Node
	if err := r.db.WithContext(ctx).Where("status = ?", models.NodeStatusActive).Find(&nodes).Error; err != nil {
		return nil, err
	}
	return nodes, nil
}

// Create inserts a new node
func (r *NodeRepository) Create(ctx context.Context, node *models.Node) error {
	return r.base.Create(ctx, node)
}

// Update updates an existing node
func (r *NodeRepository) Update(ctx context.Context, node *models.Node) error {
	return r.base.Update(ctx, node)
}

// Delete removes a node by ID (hard delete as per PRD compliance requirements)
func (r *NodeRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of nodes
func (r *NodeRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByStatus returns the number of nodes with a specific status
func (r *NodeRepository) CountByStatus(ctx context.Context, status models.NodeStatus) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Node{}).Where("status = ?", status).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateStatus updates a node's status
func (r *NodeRepository) UpdateStatus(ctx context.Context, id string, status models.NodeStatus) error {
	return r.db.WithContext(ctx).Model(&models.Node{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateToken updates a node's authentication token
func (r *NodeRepository) UpdateToken(ctx context.Context, id string, token string) error {
	return r.db.WithContext(ctx).Model(&models.Node{}).Where("id = ?", id).Update("token", token).Error
}

// GetCertFingerprint returns the node's pinned TLS cert fingerprint ("" if unset).
func (r *NodeRepository) GetCertFingerprint(ctx context.Context, id string) (string, error) {
	var fp string
	err := r.db.WithContext(ctx).Model(&models.Node{}).Where("id = ?", id).Pluck("cert_fingerprint", &fp).Error
	return fp, err
}

// SetCertFingerprint records the node's pinned TLS cert fingerprint (trust on
// first use). It only sets it when currently empty, so a concurrent first
// connection can't clobber an already-pinned value.
func (r *NodeRepository) SetCertFingerprint(ctx context.Context, id, fingerprint string) error {
	return r.db.WithContext(ctx).Model(&models.Node{}).
		Where("id = ? AND (cert_fingerprint = '' OR cert_fingerprint IS NULL)", id).
		Update("cert_fingerprint", fingerprint).Error
}

// NameExists checks if a node name already exists
func (r *NodeRepository) NameExists(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Node{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// IPAddressExists checks if an IP address is already registered
func (r *NodeRepository) IPAddressExists(ctx context.Context, ipAddress string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.Node{}).Where("ip_address = ?", ipAddress).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetIDs returns all node IDs
func (r *NodeRepository) GetIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.Node{}).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByStatus returns all node IDs filtered by status
func (r *NodeRepository) GetIDsByStatus(ctx context.Context, status models.NodeStatus) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.Node{}).Where("status = ?", status).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
