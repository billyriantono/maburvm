package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IPAMRepository provides data access for IP pools and addresses.
type IPAMRepository struct {
	db *gorm.DB
}

func NewIPAMRepository(db *gorm.DB) *IPAMRepository {
	return &IPAMRepository{db: db}
}

func (r *IPAMRepository) WithDB(db *gorm.DB) *IPAMRepository {
	return &IPAMRepository{db: db}
}

// --- Pool CRUD ---

func (r *IPAMRepository) CreatePool(ctx context.Context, pool *models.IPPool) error {
	if err := r.db.WithContext(ctx).Create(pool).Error; err != nil {
		return err
	}
	// Insert junction rows
	return r.setPoolNodes(ctx, pool.ID, pool.NodeIDs)
}

func (r *IPAMRepository) GetPool(ctx context.Context, id string) (*models.IPPool, error) {
	var pool models.IPPool
	if err := r.db.WithContext(ctx).First(&pool, "id = ?", id).Error; err != nil {
		return nil, err
	}
	nodeIDs, err := r.getPoolNodeIDs(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	pool.NodeIDs = nodeIDs
	return &pool, nil
}

func (r *IPAMRepository) ListPools(ctx context.Context, limit, offset int) ([]models.IPPool, error) {
	var pools []models.IPPool
	query := r.db.WithContext(ctx).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&pools).Error; err != nil {
		return nil, err
	}
	// Load node IDs for each pool
	for i := range pools {
		nodeIDs, err := r.getPoolNodeIDs(ctx, pools[i].ID)
		if err != nil {
			return nil, err
		}
		pools[i].NodeIDs = nodeIDs
	}
	return pools, nil
}

func (r *IPAMRepository) ListPoolsForNode(ctx context.Context, nodeID string) ([]models.IPPool, error) {
	var pools []models.IPPool
	query := r.db.WithContext(ctx).Order("created_at DESC")
	if nodeID != "" {
		// Pools assigned to this node via junction table OR legacy node_id, OR global (no assignment)
		query = query.Where(`
			(id IN (SELECT pool_id FROM ip_pool_nodes WHERE node_id = ?))
			OR (node_id = ?)
			OR (node_id IS NULL AND id NOT IN (SELECT pool_id FROM ip_pool_nodes))
		`, nodeID, nodeID)
	}
	if err := query.Find(&pools).Error; err != nil {
		return nil, err
	}
	for i := range pools {
		nodeIDs, err := r.getPoolNodeIDs(ctx, pools[i].ID)
		if err != nil {
			return nil, err
		}
		pools[i].NodeIDs = nodeIDs
	}
	return pools, nil
}

func (r *IPAMRepository) UpdatePool(ctx context.Context, pool *models.IPPool) error {
	return r.db.WithContext(ctx).Save(pool).Error
}

func (r *IPAMRepository) UpdatePoolNodes(ctx context.Context, poolID string, nodeIDs []string) error {
	return r.setPoolNodes(ctx, poolID, nodeIDs)
}

func (r *IPAMRepository) DeletePool(ctx context.Context, id string) error {
	// Junction rows cascade via FK, but explicit delete for safety
	r.db.WithContext(ctx).Exec("DELETE FROM ip_pool_nodes WHERE pool_id = ?", id)
	return r.db.WithContext(ctx).Delete(&models.IPPool{}, "id = ?", id).Error
}

// --- Address CRUD ---

func (r *IPAMRepository) CreateAddress(ctx context.Context, address *models.IPAddress) error {
	return r.db.WithContext(ctx).Create(address).Error
}

func (r *IPAMRepository) GetAddress(ctx context.Context, id string) (*models.IPAddress, error) {
	var address models.IPAddress
	if err := r.db.WithContext(ctx).First(&address, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &address, nil
}

func (r *IPAMRepository) ListAddresses(ctx context.Context, poolID string, limit, offset int) ([]models.IPAddress, error) {
	var addresses []models.IPAddress
	query := r.db.WithContext(ctx).Where("pool_id = ?", poolID).Order("address ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	return addresses, query.Find(&addresses).Error
}

// FindAvailableAddressForUpdate finds an address using a row lock. Call inside a transaction.
func (r *IPAMRepository) FindAvailableAddressForUpdate(ctx context.Context, poolID string, nodeID *string) (*models.IPAddress, error) {
	var address models.IPAddress
	query := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("pool_id = ? AND status = ?", poolID, models.IPAddressStatusAvailable)
	if nodeID != nil && *nodeID != "" {
		query = query.Where("(node_id IS NULL OR node_id = ?)", *nodeID)
	}
	if err := query.Order("address ASC").First(&address).Error; err != nil {
		return nil, err
	}
	return &address, nil
}

// FindAddressForUpdate finds a specific address in a pool using a row lock. Call inside a transaction.
func (r *IPAMRepository) FindAddressForUpdate(ctx context.Context, poolID string, address string) (*models.IPAddress, error) {
	var ip models.IPAddress
	if err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("pool_id = ? AND address = ?", poolID, address).
		First(&ip).Error; err != nil {
		return nil, err
	}
	return &ip, nil
}

func (r *IPAMRepository) ListAddressesByVMID(ctx context.Context, vmID string) ([]models.IPAddress, error) {
	var addresses []models.IPAddress
	return addresses, r.db.WithContext(ctx).Where("vm_id = ?", vmID).Find(&addresses).Error
}

func (r *IPAMRepository) ReleaseAddressesByVMID(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Model(&models.IPAddress{}).
		Where("vm_id = ? AND status IN ?", vmID, []string{models.IPAddressStatusAssigned, models.IPAddressStatusReserved}).
		Updates(map[string]interface{}{
			"status": models.IPAddressStatusAvailable,
			"vm_id":  nil,
		}).Error
}

func (r *IPAMRepository) UpdateAddress(ctx context.Context, address *models.IPAddress) error {
	return r.db.WithContext(ctx).Save(address).Error
}

func (r *IPAMRepository) UpdateAddressStatus(ctx context.Context, id string, status string, vmID *string) error {
	return r.db.WithContext(ctx).Model(&models.IPAddress{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status": status,
		"vm_id":  vmID,
	}).Error
}

func (r *IPAMRepository) DeleteAddress(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.IPAddress{}, "id = ?", id).Error
}

// --- Junction table helpers ---

type ipPoolNode struct {
	PoolID string `gorm:"primaryKey"`
	NodeID string `gorm:"primaryKey"`
}

func (ipPoolNode) TableName() string { return "ip_pool_nodes" }

func (r *IPAMRepository) getPoolNodeIDs(ctx context.Context, poolID string) ([]string, error) {
	var rows []ipPoolNode
	err := r.db.WithContext(ctx).Where("pool_id = ?", poolID).Find(&rows).Error
	if err != nil {
		// If junction table doesn't exist yet (pre-migration 007), return empty gracefully
		if isUndefinedTableError(err) {
			return nil, nil
		}
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.NodeID)
	}
	return ids, nil
}

func (r *IPAMRepository) setPoolNodes(ctx context.Context, poolID string, nodeIDs []string) error {
	// Delete existing (ignore error if table doesn't exist yet)
	if err := r.db.WithContext(ctx).Where("pool_id = ?", poolID).Delete(&ipPoolNode{}).Error; err != nil {
		if isUndefinedTableError(err) {
			return nil
		}
		return err
	}
	// Insert new
	for _, nid := range nodeIDs {
		if nid == "" {
			continue
		}
		row := ipPoolNode{PoolID: poolID, NodeID: nid}
		if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// isUndefinedTableError checks if a Postgres error is "relation does not exist"
func isUndefinedTableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "42P01") || contains(msg, "does not exist")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// GetAssignedIPMap returns a map of IP address -> VM ID from the networks table.
// Used to cross-reference which IPs are already in use by VMs.
func (r *IPAMRepository) GetAssignedIPMap(ctx context.Context) (map[string]string, error) {
	type networkRow struct {
		IPAddress string `gorm:"column:ip_address"`
		VMID      string `gorm:"column:vm_id"`
	}
	var rows []networkRow
	err := r.db.WithContext(ctx).Table("networks").
		Select("ip_address, vm_id").
		Where("deleted_at IS NULL").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		// Strip CIDR suffix if stored as inet (e.g. "198.51.100.50/32" -> "198.51.100.50")
		ip := row.IPAddress
		if idx := len(ip) - 1; idx > 0 {
			for i := range ip {
				if ip[i] == '/' {
					ip = ip[:i]
					break
				}
			}
		}
		result[ip] = row.VMID
	}
	return result, nil
}
