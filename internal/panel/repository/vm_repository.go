package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// VMRepository provides data access for virtual machines
type VMRepository struct {
	base *BaseRepository[models.VM]
	db   *gorm.DB
}

// VMWithRelations represents a VM with all its related entities loaded
type VMWithRelations struct {
	models.VM
	User *models.User `json:"user,omitempty"`
	Node *models.Node `json:"node,omitempty"`
}

// NewVMRepository creates a new VMRepository instance
func NewVMRepository(db *gorm.DB) *VMRepository {
	return &VMRepository{
		base: NewBaseRepository[models.VM](db),
		db:   db,
	}
}

// WithDB returns a VMRepository bound to the supplied database handle/transaction.
func (r *VMRepository) WithDB(db *gorm.DB) *VMRepository {
	return NewVMRepository(db)
}

// GetByID retrieves a VM by ID
func (r *VMRepository) GetByID(ctx context.Context, id string) (*models.VM, error) {
	return r.base.GetByID(ctx, id)
}

// GetByIDWithRelations retrieves a VM by ID with User, Node, and all related entities eagerly loaded
func (r *VMRepository) GetByIDWithRelations(ctx context.Context, id string) (*VMWithRelations, error) {
	var vm VMWithRelations
	if err := r.db.WithContext(ctx).
		Table("vms").
		First(&vm.VM, "id = ?", id).Error; err != nil {
		return nil, err
	}

	// Load User separately
	if vm.VM.UserID != "" {
		var user models.User
		if err := r.db.WithContext(ctx).First(&user, "id = ?", vm.VM.UserID).Error; err == nil {
			vm.User = &user
		}
	}

	// Load Node separately
	if vm.VM.NodeID != "" {
		var node models.Node
		if err := r.db.WithContext(ctx).First(&node, "id = ?", vm.VM.NodeID).Error; err == nil {
			vm.Node = &node
		}
	}

	return &vm, nil
}

// GetByIDWithUser retrieves a VM by ID with User eagerly loaded
func (r *VMRepository) GetByIDWithUser(ctx context.Context, id string) (*models.VM, error) {
	var vm models.VM
	if err := r.db.WithContext(ctx).First(&vm, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &vm, nil
}

// GetByIDWithNode retrieves a VM by ID with Node eagerly loaded
func (r *VMRepository) GetByIDWithNode(ctx context.Context, id string) (*models.VM, error) {
	var vm models.VM
	if err := r.db.WithContext(ctx).First(&vm, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &vm, nil
}

// GetByIDWithNetworks retrieves a VM by ID with Networks eagerly loaded
func (r *VMRepository) GetByIDWithNetworks(ctx context.Context, id string) (*models.VM, error) {
	var vm models.VM
	if err := r.db.WithContext(ctx).First(&vm, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &vm, nil
}

// GetByIDWithFirewalls retrieves a VM by ID with FirewallRules eagerly loaded
func (r *VMRepository) GetByIDWithFirewalls(ctx context.Context, id string) (*models.VM, error) {
	var vm models.VM
	if err := r.db.WithContext(ctx).First(&vm, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &vm, nil
}

// List retrieves all VMs with optional pagination
func (r *VMRepository) List(ctx context.Context, limit, offset int) ([]models.VM, error) {
	return r.base.List(ctx, limit, offset)
}

// ListWithRelations retrieves all VMs with User and Node eagerly loaded
func (r *VMRepository) ListWithRelations(ctx context.Context, limit, offset int) ([]VMWithRelations, error) {
	var vms []VMWithRelations
	query := r.db.WithContext(ctx)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	return vms, nil
}

// ListByUserID retrieves VMs filtered by user ID with optional pagination
func (r *VMRepository) ListByUserID(ctx context.Context, userID string, limit, offset int) ([]models.VM, error) {
	var vms []models.VM
	query := r.db.WithContext(ctx).Where("user_id = ?", userID)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	return vms, nil
}

// ListByNodeID retrieves VMs filtered by node ID with optional pagination
func (r *VMRepository) ListByNodeID(ctx context.Context, nodeID string, limit, offset int) ([]models.VM, error) {
	var vms []models.VM
	query := r.db.WithContext(ctx).Where("node_id = ?", nodeID)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	return vms, nil
}

// ListByStatus retrieves VMs filtered by status with optional pagination
func (r *VMRepository) ListByStatus(ctx context.Context, status models.VMStatus, limit, offset int) ([]models.VM, error) {
	var vms []models.VM
	query := r.db.WithContext(ctx).Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	return vms, nil
}

// ListByTemplateID retrieves VMs filtered by OS template ID with optional pagination
func (r *VMRepository) ListByTemplateID(ctx context.Context, templateID string, limit, offset int) ([]models.VM, error) {
	var vms []models.VM
	query := r.db.WithContext(ctx).Where("os_template_id = ?", templateID)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&vms).Error; err != nil {
		return nil, err
	}
	return vms, nil
}

// Create inserts a new VM
func (r *VMRepository) Create(ctx context.Context, vm *models.VM) error {
	return r.base.Create(ctx, vm)
}

// Update updates an existing VM
func (r *VMRepository) Update(ctx context.Context, vm *models.VM) error {
	return r.base.Update(ctx, vm)
}

// Delete removes a VM by ID (hard delete as per PRD compliance requirements)
func (r *VMRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of VMs
func (r *VMRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByUserID returns the number of VMs owned by a user
func (r *VMRepository) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByNodeID returns the number of VMs on a node
func (r *VMRepository) CountByNodeID(ctx context.Context, nodeID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("node_id = ?", nodeID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByStatus returns the number of VMs with a specific status
func (r *VMRepository) CountByStatus(ctx context.Context, status models.VMStatus) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("status = ?", status).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateStatus updates a VM's status
func (r *VMRepository) UpdateStatus(ctx context.Context, id string, status models.VMStatus) error {
	return r.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateNodeID updates a VM's node assignment
func (r *VMRepository) UpdateNodeID(ctx context.Context, id string, nodeID string) error {
	return r.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", id).Update("node_id", nodeID).Error
}

// UpdateVNCPort updates a VM's VNC port
func (r *VMRepository) UpdateVNCPort(ctx context.Context, id string, port int) error {
	return r.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", id).Update("vnc_port", port).Error
}

// UpdateVNCPassword updates a VM's VNC password
func (r *VMRepository) UpdateVNCPassword(ctx context.Context, id string, password string) error {
	return r.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", id).Update("vnc_password", password).Error
}

// UpdateResources updates a VM's resource allocation
func (r *VMRepository) UpdateResources(ctx context.Context, id string, resources models.Resources) error {
	return r.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", id).Update("resources", resources).Error
}

// GetByHostname retrieves a VM by hostname
func (r *VMRepository) GetByHostname(ctx context.Context, hostname string) (*models.VM, error) {
	var vm models.VM
	if err := r.db.WithContext(ctx).Where("hostname = ?", hostname).First(&vm).Error; err != nil {
		return nil, err
	}
	return &vm, nil
}

// HostnameExists checks if a hostname is already in use
func (r *VMRepository) HostnameExists(ctx context.Context, hostname string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("hostname = ?", hostname).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetIDs returns all VM IDs
func (r *VMRepository) GetIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByUserID returns all VM IDs owned by a user
func (r *VMRepository) GetIDsByUserID(ctx context.Context, userID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("user_id = ?", userID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByNodeID returns all VM IDs on a specific node
func (r *VMRepository) GetIDsByNodeID(ctx context.Context, nodeID string) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("node_id = ?", nodeID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByStatus returns all VM IDs with a specific status
func (r *VMRepository) GetIDsByStatus(ctx context.Context, status models.VMStatus) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.VM{}).Where("status = ?", status).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
