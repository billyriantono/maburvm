package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPortForwardTestService(t *testing.T) (*NetworkService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:pf-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE networks (
		id TEXT PRIMARY KEY, vm_id TEXT, ip_address TEXT, bandwidth_limit INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE port_forwards (
		id TEXT PRIMARY KEY, vm_id TEXT, network_id TEXT,
		external_port INTEGER, internal_port INTEGER, protocol TEXT, source_ip TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)

	svc := NewNetworkService(
		db,
		repository.NewNetworkRepository(db),
		repository.NewFirewallRepository(db),
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		nil, // riverClient — not exercised by the query-only methods under test
	)
	return svc, db
}

func TestPrimaryNetworkID(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	ctx := context.Background()

	// No interface yet → ErrNetworkNotFound.
	_, err := svc.primaryNetworkID(ctx, "vm-1")
	require.ErrorIs(t, err, ErrNetworkNotFound)

	require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id, ip_address) VALUES ('net-1','vm-1','10.0.0.5')`).Error)
	id, err := svc.primaryNetworkID(ctx, "vm-1")
	require.NoError(t, err)
	require.Equal(t, "net-1", id)
}

func TestGetPortForwardsForVM(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	ctx := context.Background()

	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, external_port, internal_port, protocol, source_ip)
		VALUES ('pf-1','vm-1','net-1',8080,80,'tcp','0.0.0.0/0'),
		       ('pf-2','vm-1','net-1',2222,22,'tcp','0.0.0.0/0'),
		       ('pf-3','vm-2','net-9',9090,90,'tcp','0.0.0.0/0')`).Error)

	forwards, err := svc.GetPortForwardsForVM(ctx, "vm-1")
	require.NoError(t, err)
	require.Len(t, forwards, 2, "should return only this VM's forwards")
	for _, f := range forwards {
		require.Equal(t, "vm-1", f.VMID)
	}

	// A VM with no forwards returns an empty slice, not an error.
	none, err := svc.GetPortForwardsForVM(ctx, "vm-3")
	require.NoError(t, err)
	require.Empty(t, none)
}
