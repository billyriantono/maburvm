package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupIPAMServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:ipam-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE ip_pools (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		node_id TEXT,
		family TEXT NOT NULL DEFAULT 'ipv4',
		cidr TEXT,
		gateway TEXT,
		bridge TEXT,
		range_start TEXT,
		range_end TEXT,
		description TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE ip_addresses (
		id TEXT PRIMARY KEY,
		pool_id TEXT NOT NULL,
		node_id TEXT,
		address TEXT NOT NULL,
		family TEXT NOT NULL DEFAULT 'ipv4',
		status TEXT NOT NULL DEFAULT 'available',
		vm_id TEXT,
		delivery_mode TEXT NOT NULL DEFAULT 'direct',
		nat_mode TEXT NOT NULL DEFAULT '',
		user_id TEXT,
		note TEXT,
		rdns TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE ip_pool_nodes (
		pool_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		PRIMARY KEY (pool_id, node_id)
	)`).Error)
	return db
}

func TestIPAMServiceAllocateAddressAssignsFirstAvailable(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()

	// No CIDR: avoid auto-generating the whole range so this test exercises
	// allocation ordering over exactly the two addresses added below.
	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "public-v4", Family: models.IPFamilyIPv4})
	require.NoError(t, err)
	_, err = svc.AddAddress(ctx, pool.ID, &CreateIPAddressRequest{Address: "192.0.2.11"})
	require.NoError(t, err)
	_, err = svc.AddAddress(ctx, pool.ID, &CreateIPAddressRequest{Address: "192.0.2.10"})
	require.NoError(t, err)

	vmID := "11111111-1111-1111-1111-111111111111"
	allocated, err := svc.AllocateAddress(ctx, &AllocateIPAddressRequest{PoolID: pool.ID, VMID: &vmID})
	require.NoError(t, err)
	require.Equal(t, "192.0.2.10", allocated.Address)
	require.Equal(t, models.IPAddressStatusAssigned, allocated.Status)
	require.Equal(t, &vmID, allocated.VMID)
}

func TestIPAMServiceAllocateAddressReturnsNoAvailable(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()

	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "public-v4"})
	require.NoError(t, err)
	_, err = svc.AllocateAddress(ctx, &AllocateIPAddressRequest{PoolID: pool.ID})
	require.ErrorIs(t, err, ErrNoAvailableIPAddress)
}

// TestIPAMServiceAllocateRejectsWrongNode covers the case the user hit: a pool
// with plenty of free addresses but bound to a different node. The allocator
// must return the distinct ErrPoolNotAvailableOnNode (not "no available IP").
func TestIPAMServiceAllocateRejectsWrongNode(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()

	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "node-bound", Family: models.IPFamilyIPv4})
	require.NoError(t, err)
	_, err = svc.AddAddress(ctx, pool.ID, &CreateIPAddressRequest{Address: "192.0.2.10"})
	require.NoError(t, err)

	const nodeA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const nodeB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	require.NoError(t, db.Exec(`INSERT INTO ip_pool_nodes (pool_id, node_id) VALUES (?, ?)`, pool.ID, nodeA).Error)

	vmID := "11111111-1111-1111-1111-111111111111"

	// Wrong node → distinct, clear error (even though an address is free).
	nodeB2 := nodeB
	_, err = svc.AllocateAddress(ctx, &AllocateIPAddressRequest{PoolID: pool.ID, NodeID: &nodeB2, VMID: &vmID})
	require.ErrorIs(t, err, ErrPoolNotAvailableOnNode)

	// Matching node → succeeds.
	nodeA2 := nodeA
	allocated, err := svc.AllocateAddress(ctx, &AllocateIPAddressRequest{PoolID: pool.ID, NodeID: &nodeA2, VMID: &vmID})
	require.NoError(t, err)
	require.Equal(t, "192.0.2.10", allocated.Address)
}

func TestIPAMServiceRejectsFamilyMismatch(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()

	pool, err := svc.CreatePool(ctx, &CreateIPPoolRequest{Name: "public-v6", Family: models.IPFamilyIPv6, CIDR: "2001:db8::/64"})
	require.NoError(t, err)
	_, err = svc.AddAddress(ctx, pool.ID, &CreateIPAddressRequest{Address: "192.0.2.10", Family: models.IPFamilyIPv4})
	require.ErrorIs(t, err, ErrInvalidIPFamily)
}
