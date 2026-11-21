package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
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
	// lvmVerdict is what the node says about an LVM pool; checked counts asks.
	lvmVerdict error
	checked    int
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

func (f *fakeProvisioner) DeleteVolume(_ context.Context, _ *models.Node, poolType, poolPath, _ string) error {
	f.deleteCalled = true
	f.lastPoolType, f.lastPoolPath = poolType, poolPath
	return f.deleteErr
}

func (f *fakeProvisioner) CheckLVMPool(_ context.Context, _ *models.Node, poolType, poolPath string) error {
	f.checked++
	f.lastPoolType, f.lastPoolPath = poolType, poolPath
	return f.lvmVerdict
}

func setupStorageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Explicit SQL (not AutoMigrate): the models carry Postgres-only defaults
	// (gen_random_uuid()/NOW()) that sqlite cannot parse at table creation.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:storage-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL, cert_fingerprint TEXT NOT NULL DEFAULT '', region_id TEXT, zone TEXT NOT NULL DEFAULT '', memory_total_bytes INTEGER NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT, status TEXT, total_space INTEGER DEFAULT 0, used_space INTEGER DEFAULT 0, available_space INTEGER DEFAULT 0, path TEXT NOT NULL, file_format TEXT, alert_threshold INTEGER DEFAULT 90, overcommit INTEGER DEFAULT 0, is_primary BOOLEAN DEFAULT 0, status_note TEXT NOT NULL DEFAULT '', metadata_percent REAL, node_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
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

// An LVM pool its node does not report usable (an agent rolled back to one
// without LVM pools) is never asked to make or remove a volume.
func TestVolumesRefusedOnUnusableLVMPool(t *testing.T) {
	db := setupStorageTestDB(t)
	pool := seedStoragePool(t, db)
	require.NoError(t, db.Model(pool).Updates(map[string]any{"type": models.PoolTypeLVM, "path": "vg0", "status": "degraded", "status_note": "update the agent"}).Error)
	require.NoError(t, db.Create(&models.StorageVolume{ID: "33333333-3333-3333-3333-333333333333", Name: "kept", PoolID: pool.ID, Size: bytesPerGB, Path: "/dev/vg0/legacy1"}).Error)
	fake := &fakeProvisioner{}
	svc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), fake)

	require.ErrorContains(t, svc.CreateVolume(&models.StorageVolume{Name: "v", PoolID: pool.ID, Size: bytesPerGB}), "update the agent")
	require.ErrorContains(t, svc.DeleteVolume("33333333-3333-3333-3333-333333333333"), "update the agent")
	require.False(t, fake.createCalled || fake.deleteCalled, "the node was asked")

	require.NoError(t, db.Model(pool).Update("status", "online").Error)
	require.NoError(t, svc.DeleteVolume("33333333-3333-3333-3333-333333333333"))
	require.Equal(t, "vg0", fake.lastPoolPath)
}

func TestCreatePoolOnlyRegistersWhatCanHoldDisks(t *testing.T) {
	db := setupStorageTestDB(t)
	svc := NewStorageService(repository.NewStorageRepository(db))
	const node = "11111111-1111-1111-1111-111111111111"

	for _, typ := range []string{"ceph", "nfs", "zfs", "zfsthin", "zfscompressed"} {
		err := svc.CreatePool(&models.StoragePool{ID: "p-" + typ, Name: typ, NodeID: node, Path: "/data", Type: typ})
		assert.ErrorIs(t, err, ErrInvalidStoragePool, "%s has no backend on the node and must not become a row", typ)
	}
	// LVM is real, but only once its node has confirmed it; here nothing can.
	for _, typ := range []string{"lvm", "lvmthin"} {
		err := svc.CreatePool(&models.StoragePool{ID: "p-" + typ, Name: typ, NodeID: node, Path: "vg0/thin", Type: typ})
		assert.ErrorIs(t, err, ErrInvalidStoragePool, "%s registered without asking the node", typ)
	}
	assert.ErrorIs(t, svc.CreatePool(&models.StoragePool{ID: "p-rel", Name: "rel", NodeID: node, Path: "data"}), ErrInvalidStoragePool)
	var count int64
	require.NoError(t, db.Model(&models.StoragePool{}).Count(&count).Error)
	assert.Zero(t, count)

	// Capacity is measured on the node, so a size in the request is not kept.
	first := &models.StoragePool{ID: "p-1", Name: "data", NodeID: node, Path: "/data/", TotalSpace: 500 * gib, AvailableSpace: 500 * gib, Status: "online", IsPrimary: true}
	require.NoError(t, svc.CreatePool(first))
	var stored models.StoragePool
	require.NoError(t, db.First(&stored, "id = ?", "p-1").Error)
	assert.Equal(t, "dir", stored.Type)
	assert.Equal(t, "/data", stored.Path)
	assert.Zero(t, stored.TotalSpace)
	assert.Equal(t, "offline", stored.Status, "not measured yet is not online")
	assert.False(t, stored.IsPrimary, "an unmeasured pool must not take over provisioning from one that works")
	assert.ErrorIs(t, svc.UpdatePool("p-1", &models.StoragePool{IsPrimary: true}), ErrInvalidStoragePool, "nor be promoted before the node has seen it")
	assert.ErrorIs(t, svc.CreatePool(&models.StoragePool{ID: "p-2", Name: "again", NodeID: node, Path: "/data"}), ErrInvalidStoragePool, "one directory is one pool")
	assert.NoError(t, svc.CreatePool(&models.StoragePool{ID: "p-3", Name: "data", NodeID: "another-node", Path: "/data"}), "the same path on another node is another directory")
}

func TestPoolMeasurementAndEditsDoNotOverwriteEachOther(t *testing.T) {
	db := setupStorageTestDB(t)
	pool := seedStoragePool(t, db)
	repo := repository.NewStorageRepository(db)
	svc := NewStorageService(repo)

	// The collector read the pool, then the operator renamed it and made it
	// primary while the node was being asked.
	stale := *pool
	require.NoError(t, svc.UpdatePool(pool.ID, &models.StoragePool{Name: "renamed", IsPrimary: true, TotalSpace: 1}))
	stale.TotalSpace, stale.UsedSpace, stale.AvailableSpace, stale.Status = 900*gib, 300*gib, 600*gib, "online"
	require.NoError(t, repo.UpdatePoolMeasurement(&stale))

	var stored models.StoragePool
	require.NoError(t, db.First(&stored, "id = ?", pool.ID).Error)
	assert.Equal(t, "renamed", stored.Name)
	assert.True(t, stored.IsPrimary)
	assert.Equal(t, 900*gib, stored.TotalSpace, "capacity is what the node measured, not what a request said")

	// And an edit after a measurement keeps the measurement.
	require.NoError(t, svc.UpdatePool(pool.ID, &models.StoragePool{AlertThreshold: 80, TotalSpace: 5 * gib}))
	require.NoError(t, db.First(&stored, "id = ?", pool.ID).Error)
	assert.Equal(t, 80, stored.AlertThreshold)
	assert.Equal(t, 900*gib, stored.TotalSpace)
	assert.Equal(t, "online", stored.Status)
}

// A node left with two primaries (written by an older panel) is put right by
// promoting one of them, and creating a pool as primary demotes the rest.
func TestOnlyOnePrimaryPoolPerNode(t *testing.T) {
	db := setupStorageTestDB(t)
	repo := repository.NewStorageRepository(db)
	svc := NewStorageService(repo)
	const node = "node-1"
	primaries := func() (ids []string) {
		require.NoError(t, db.Model(&models.StoragePool{}).Where("node_id = ? AND is_primary", node).Order("id").Pluck("id", &ids).Error)
		return ids
	}

	require.NoError(t, db.Create(&models.StoragePool{ID: "p-root", Name: "local", NodeID: node, Path: DefaultNodeImageDir, Type: "dir", Status: "online", IsPrimary: true}).Error)
	require.NoError(t, db.Create(&models.StoragePool{ID: "p-vm", Name: "vm", NodeID: node, Path: "/vm", Type: "dir", Status: "online", IsPrimary: true}).Error)
	require.NoError(t, db.Create(&models.StoragePool{ID: "p-far", Name: "far", NodeID: "node-2", Path: "/vm", Type: "dir", Status: "online", IsPrimary: true}).Error)

	require.NoError(t, svc.UpdatePool("p-vm", &models.StoragePool{IsPrimary: true}))
	assert.Equal(t, []string{"p-vm"}, primaries(), "re-promoting an already-primary pool must demote the other")

	require.NoError(t, repo.CreatePool(&models.StoragePool{ID: "p-new", Name: "new", NodeID: node, Path: "/new", Type: "dir", IsPrimary: true}))
	assert.Equal(t, []string{"p-new"}, primaries(), "a pool created primary must demote the rest")

	// Promoting a pool that is gone, or on another node, demotes nothing.
	require.NoError(t, repo.DeletePool("p-root"))
	assert.ErrorIs(t, repo.SetPrimaryPool(node, "p-root"), repository.ErrStoragePoolNotOnNode)
	assert.ErrorIs(t, repo.SetPrimaryPool(node, "p-far"), repository.ErrStoragePoolNotOnNode)
	assert.Equal(t, []string{"p-new"}, primaries(), "a failed promotion must leave the node its primary")

	var far models.StoragePool
	require.NoError(t, db.First(&far, "id = ?", "p-far").Error)
	assert.True(t, far.IsPrimary, "another node's primary is untouched")
}

func TestCreateLVMPoolAsksTheNode(t *testing.T) {
	db := setupStorageTestDB(t)
	seed := seedStoragePool(t, db)
	fake := &fakeProvisioner{}
	svc := NewStorageServiceWithProvisioner(repository.NewStorageRepository(db), repository.NewNodeRepository(db), fake)

	thin := &models.StoragePool{ID: "p-thin", Name: "fast", NodeID: seed.NodeID, Type: models.PoolTypeLVMThin, Path: " vg0/thin "}
	require.NoError(t, svc.CreatePool(thin))
	assert.Equal(t, 1, fake.checked)
	assert.Equal(t, "vg0/thin", fake.lastPoolPath)
	var stored models.StoragePool
	require.NoError(t, db.First(&stored, "id = ?", "p-thin").Error)
	assert.Equal(t, "vg0/thin", stored.Path)
	assert.Equal(t, "raw", stored.FileFormat)
	assert.Equal(t, "offline", stored.Status, "measured by the collector, not here")

	// A thick pool on the same group is another pool; the same thin pool twice is not.
	require.NoError(t, svc.CreatePool(&models.StoragePool{ID: "p-thick", Name: "slow", NodeID: seed.NodeID, Type: models.PoolTypeLVM, Path: "vg0"}))
	assert.ErrorIs(t, svc.CreatePool(&models.StoragePool{ID: "p-dup", Name: "dup", NodeID: seed.NodeID, Type: models.PoolTypeLVMThin, Path: "vg0/thin"}), ErrInvalidStoragePool)

	// Bad names never reach the node; the node's refusal is the operator's message.
	before := fake.checked
	for _, bad := range []models.StoragePool{
		{Name: "a", Type: models.PoolTypeLVMThin, Path: "vg0"},
		{Name: "b", Type: models.PoolTypeLVM, Path: "/dev/vg0"},
		{Name: "c", Type: models.PoolTypeLVM, Path: "-vg"},
	} {
		bad.ID, bad.NodeID = "p-"+bad.Name, seed.NodeID
		assert.ErrorIs(t, svc.CreatePool(&bad), ErrInvalidStoragePool, "%+v", bad)
	}
	assert.Equal(t, before, fake.checked)
	fake.lvmVerdict = errors.New("volume group ubuntu-vg holds this host's own system")
	err := svc.CreatePool(&models.StoragePool{ID: "p-os", Name: "os", NodeID: seed.NodeID, Type: models.PoolTypeLVM, Path: "ubuntu-vg"})
	assert.ErrorIs(t, err, ErrInvalidStoragePool)
	assert.Contains(t, err.Error(), "host's own system")
}

func TestLVMPoolVerdict(t *testing.T) {
	assert.ErrorContains(t, lvmPoolVerdict("n1", "vg0", nil), "update it", "an agent that predates LVM reports nothing")
	assert.ErrorContains(t, lvmPoolVerdict("n1", "vg0/t", []*pb.LVMPoolCapacity{{Exists: true, Error: "vg0/t is not a thin pool"}}), "not a thin pool")
	assert.ErrorContains(t, lvmPoolVerdict("n1", "vg0", []*pb.LVMPoolCapacity{{}}), "does not exist")
	assert.NoError(t, lvmPoolVerdict("n1", "vg0", []*pb.LVMPoolCapacity{{Exists: true, TotalBytes: 1}}))
}

func TestMeasureLVMPool(t *testing.T) {
	report := map[string]*pb.LVMPoolCapacity{
		"vg0/thin": {Vg: "vg0", ThinPool: "thin", Exists: true, TotalBytes: 100 * gib, UsedBytes: 25 * gib, AvailableBytes: 75 * gib, MetadataPercent: 81.5},
		"vg1/":     {Vg: "vg1", Exists: true, TotalBytes: 50 * gib, UsedBytes: 10 * gib, AvailableBytes: 40 * gib, Error: "volume group vg1 holds this host's own system"},
	}
	thin := models.StoragePool{Type: models.PoolTypeLVMThin, Path: "vg0/thin"}
	measureLVMPool(&thin, report)
	assert.Equal(t, "online", thin.Status)
	assert.Equal(t, 75*gib, thin.AvailableSpace)
	require.NotNil(t, thin.MetadataPercent)
	assert.Equal(t, 81.5, *thin.MetadataPercent)

	osPool := models.StoragePool{Type: models.PoolTypeLVM, Path: "vg1"}
	measureLVMPool(&osPool, report)
	assert.Equal(t, "degraded", osPool.Status)
	assert.Contains(t, osPool.StatusNote, "host's own system")
	assert.Nil(t, osPool.MetadataPercent, "a thick pool has no metadata of its own")

	old := models.StoragePool{Type: models.PoolTypeLVM, Path: "vg2", Status: "online"}
	measureLVMPool(&old, report)
	assert.Equal(t, "degraded", old.Status, "a pool the node did not report takes no new disks")
	assert.Contains(t, old.StatusNote, "update it")
}
