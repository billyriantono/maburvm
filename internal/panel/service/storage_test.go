package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// fakeProvisioner records calls and returns canned results.
type fakeProvisioner struct {
	createCalled bool
	deleteCalled bool
	createErr    error
	deleteErr    error
	lastPoolType string
	lastPoolPath string
	lastFormat   string
	lastSizeGB   int64
}

func (f *fakeProvisioner) CreateVolume(_ context.Context, _ *models.Node, poolType, poolPath, name, format string, sizeGB int64) (string, int64, error) {
	f.createCalled = true
	f.lastPoolType = poolType
	f.lastPoolPath = poolPath
	f.lastFormat = format
	f.lastSizeGB = sizeGB
	if f.createErr != nil {
		return "", 0, f.createErr
	}
	return fmt.Sprintf("%s/%s.%s", poolPath, name, format), 1024, nil
}

func (f *fakeProvisioner) DeleteVolume(_ context.Context, _ *models.Node, _ string, _ string) error {
	f.deleteCalled = true
	return f.deleteErr
}

func setupStorageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Explicit SQL (not AutoMigrate): the models carry Postgres-only defaults
	// (gen_random_uuid()/NOW()) that sqlite cannot parse at table creation.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:storage-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL, cert_fingerprint TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT, status TEXT, total_space INTEGER DEFAULT 0, used_space INTEGER DEFAULT 0, available_space INTEGER DEFAULT 0, path TEXT NOT NULL, file_format TEXT, alert_threshold INTEGER DEFAULT 90, overcommit INTEGER DEFAULT 0, is_primary BOOLEAN DEFAULT 0, node_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_volumes (id TEXT PRIMARY KEY, name TEXT NOT NULL, pool_id TEXT NOT NULL, vm_id TEXT, size INTEGER NOT NULL DEFAULT 0, format TEXT, path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	return db
}

func seedStoragePool(t *testing.T, db *gorm.DB) *models.StoragePool {
	t.Helper()
	node := &models.Node{ID: "11111111-1111-1111-1111-111111111111", Name: "node-1", IPAddress: "10.0.0.1", Token: "tok", Status: models.NodeStatusActive}
	require.NoError(t, db.Create(node).Error)
	pool := &models.StoragePool{ID: "22222222-2222-2222-2222-222222222222", Name: "pool-1", NodeID: node.ID, Path: "/var/lib/libvirt/images", Type: "dir", Status: "online"}
	require.NoError(t, db.Create(pool).Error)
	return pool
}

func TestStorageServiceProvisionsVolume(t *testing.T) {
	db := setupStorageTestDB(t)
	pool := seedStoragePool(t, db)
	fake := &fakeProvisioner{}
	svc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), fake)

	vol := &models.StorageVolume{Name: "data1", PoolID: pool.ID, Size: 5 * bytesPerGB, Format: "qcow2"}
	require.NoError(t, svc.CreateVolume(vol))

	require.True(t, fake.createCalled, "must provision on the node")
	require.Equal(t, "dir", fake.lastPoolType, "pool type is forwarded to the agent")
	require.Equal(t, "/var/lib/libvirt/images", fake.lastPoolPath)
	require.Equal(t, int64(5), fake.lastSizeGB, "5 GiB → 5 GB")
	require.NotEmpty(t, vol.Path, "path recorded from the agent")

	// The record is persisted.
	got, err := svc.GetVolumeByID(vol.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestStorageServiceRoundsUpSizeAndStopsOnProvisionFailure(t *testing.T) {
	db := setupStorageTestDB(t)
	pool := seedStoragePool(t, db)

	// Sub-GB and non-whole sizes round up to whole GB.
	okFake := &fakeProvisioner{}
	okSvc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), okFake)
	require.NoError(t, okSvc.CreateVolume(&models.StorageVolume{Name: "rounded", PoolID: pool.ID, Size: bytesPerGB + 1}))
	require.Equal(t, int64(2), okFake.lastSizeGB, "1 GiB + 1 byte rounds up to 2 GB")

	// A provisioning failure must NOT persist a DB record.
	failFake := &fakeProvisioner{createErr: errors.New("qemu-img boom")}
	failSvc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), failFake)
	vol := &models.StorageVolume{Name: "ghost", PoolID: pool.ID, Size: bytesPerGB}
	require.Error(t, failSvc.CreateVolume(vol))

	vols, err := okSvc.GetVolumes(pool.ID)
	require.NoError(t, err)
	for _, v := range vols {
		require.NotEqual(t, "ghost", v.Name, "failed provisioning must not leave a record")
	}
}

func TestStorageServiceDeleteRemovesFileFirst(t *testing.T) {
	db := setupStorageTestDB(t)
	pool := seedStoragePool(t, db)

	// Seed a volume with a recorded path.
	created := &fakeProvisioner{}
	svc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), created)
	vol := &models.StorageVolume{Name: "todelete", PoolID: pool.ID, Size: bytesPerGB, Format: "qcow2"}
	require.NoError(t, svc.CreateVolume(vol))

	// If file deletion fails, the record is kept and an error returned.
	failDel := &fakeProvisioner{deleteErr: errors.New("rm failed")}
	failSvc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), failDel)
	require.Error(t, failSvc.DeleteVolume(vol.ID))
	require.True(t, failDel.deleteCalled)
	stillThere, err := svc.GetVolumeByID(vol.ID)
	require.NoError(t, err)
	require.NotNil(t, stillThere, "record kept when file deletion fails")

	// On success, both the file and the record go away.
	okDel := &fakeProvisioner{}
	okSvc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), okDel)
	require.NoError(t, okSvc.DeleteVolume(vol.ID))
	require.True(t, okDel.deleteCalled)
}
