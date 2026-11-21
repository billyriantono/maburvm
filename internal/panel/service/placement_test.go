package service

import (
	"context"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const gib = int64(1024 * 1024 * 1024)

func TestPoolFits(t *testing.T) {
	tests := []struct {
		name   string
		pool   *models.StoragePool
		diskGB int
		want   bool
		why    string
	}{
		{
			name:   "room for the disk and the reserve",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 214 * gib},
			diskGB: 40,
			want:   true,
		},
		{
			name:   "fits the disk but not the reserve",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 45 * gib},
			diskGB: 40,
			want:   false,
			why:    "a disk is thin on day one and grows; placing it with no headroom fills the pool later, under load",
		},
		{
			name:   "nowhere near",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 2 * gib},
			diskGB: 40,
			want:   false,
		},
		{
			name:   "unknown pool is not a full pool",
			pool:   nil,
			diskGB: 40,
			want:   true,
			why:    "the panel not having synced a node's storage must not stop orders on a healthy node",
		},
		{
			name:   "unmeasured pool is not a full pool",
			pool:   &models.StoragePool{TotalSpace: 0, AvailableSpace: 0},
			diskGB: 40,
			want:   true,
			why:    "zero total means no reading was taken, not that the disk is full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PoolFits(tt.pool, tt.diskGB); got != tt.want {
				t.Errorf("PoolFits(%v GB) = %v, want %v. %s", tt.diskGB, got, tt.want, tt.why)
			}
		})
	}
}

func TestPoolWithinRatio(t *testing.T) {
	// 900 GiB pool with 1500 GiB of disks already promised: 1.67x.
	pool := &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 600 * gib}

	tests := []struct {
		name      string
		pool      *models.StoragePool
		allocated int64
		addGB     int
		ratio     float64
		want      bool
		why       string
	}{
		{
			name: "no ratio is no cap", pool: pool, allocated: 1500 * gib, addGB: 40, ratio: 0, want: true,
			why: "live pools are already past 1.0x; an unset ratio must not start refusing orders",
		},
		{
			name: "under the cap", pool: pool, allocated: 1500 * gib, addGB: 40, ratio: 2, want: true,
		},
		{
			name: "exactly at the cap", pool: pool, allocated: 1760 * gib, addGB: 40, ratio: 2, want: true,
		},
		{
			name: "over the cap", pool: pool, allocated: 1780 * gib, addGB: 40, ratio: 2, want: false,
			why: "free space is the same for this order as for the last one; only the sum of promises says stop",
		},
		{
			name: "already over, nothing more fits", pool: pool, allocated: 1500 * gib, addGB: 1, ratio: 1, want: false,
		},
		{
			name: "unmeasured pool is within any ratio", pool: &models.StoragePool{}, allocated: 5000 * gib, addGB: 40, ratio: 1, want: true,
			why: "no reading is not evidence of a full disk",
		},
		{
			name: "unknown pool is within any ratio", pool: nil, allocated: 0, addGB: 40, ratio: 1, want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PoolWithinRatio(tt.pool, tt.allocated, tt.addGB, tt.ratio); got != tt.want {
				t.Errorf("PoolWithinRatio(%d GiB allocated, +%d GB, ratio %.1f) = %v, want %v. %s",
					tt.allocated/gib, tt.addGB, tt.ratio, got, tt.want, tt.why)
			}
		})
	}
}

// placementDB is a node with two pools: "data" (primary) and "local" on the
// default image directory.
func placementDB(t *testing.T) (*gorm.DB, *models.StoragePool, *models.StoragePool) {
	t.Helper()
	db := setupStorageTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE vms (id TEXT PRIMARY KEY, node_id TEXT NOT NULL, resources TEXT, storage_pool_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE vm_disks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, size_gb INTEGER NOT NULL, path TEXT NOT NULL, deleted_at DATETIME)`,
		`CREATE TABLE disk_quota_reservations (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, size_gb INTEGER NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	data := &models.StoragePool{ID: "pool-data", Name: "data", NodeID: "node-1", Path: "/data", Type: "dir", Status: "online", IsPrimary: true, TotalSpace: 900 * gib, AvailableSpace: 600 * gib}
	local := &models.StoragePool{ID: "pool-local", Name: "local", NodeID: "node-1", Path: DefaultNodeImageDir, Type: "dir", Status: "online", TotalSpace: 100 * gib, AvailableSpace: 80 * gib}
	require.NoError(t, db.Create(data).Error)
	require.NoError(t, db.Create(local).Error)
	return db, data, local
}

func TestPoolAllocations(t *testing.T) {
	db, data, local := placementDB(t)
	for _, stmt := range []string{
		// Recorded in the primary pool.
		`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-a', 'node-1', '{"cpu":1,"ram":1024,"disk":100}', 'pool-data')`,
		// Recorded in the other pool: must not drift to the primary.
		`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-b', 'node-1', '{"cpu":1,"ram":1024,"disk":30}', 'pool-local')`,
		// Nothing recorded, and a pool id from another node: both fall back to the primary.
		`INSERT INTO vms (id, node_id, resources) VALUES ('vm-c', 'node-1', '{"cpu":1,"ram":1024,"disk":50}')`,
		`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-d', 'node-1', '{"cpu":1,"ram":1024,"disk":20}', 'pool-elsewhere')`,
		// Deleted VMs and VMs on other nodes hold nothing here.
		`INSERT INTO vms (id, node_id, resources, storage_pool_id, deleted_at) VALUES ('vm-gone', 'node-1', '{"cpu":1,"ram":1024,"disk":500}', 'pool-data', '2026-01-01')`,
		`INSERT INTO vms (id, node_id, resources) VALUES ('vm-far', 'node-2', '{"cpu":1,"ram":1024,"disk":700}')`,
		// An extra disk counts where its file is, whatever pool its VM is in.
		`INSERT INTO vm_disks (id, vm_id, size_gb, path) VALUES ('d1', 'vm-a', 10, '/var/lib/libvirt/images/vm-a-data-1.qcow2')`,
		`INSERT INTO vm_disks (id, vm_id, size_gb, path) VALUES ('d2', 'vm-a', 15, '/data/vm-a-data-2.qcow2')`,
		`INSERT INTO vm_disks (id, vm_id, size_gb, path, deleted_at) VALUES ('d3', 'vm-a', 400, '/data/old.qcow2', '2026-01-01')`,
		// An attach in flight counts in its VM's pool; a finished one is a vm_disks row already.
		`INSERT INTO disk_quota_reservations (id, vm_id, size_gb, status) VALUES ('r1', 'vm-a', 5, 'pending')`,
		`INSERT INTO disk_quota_reservations (id, vm_id, size_gb, status) VALUES ('r2', 'vm-a', 300, 'consumed')`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	require.NoError(t, db.Create(&models.StorageVolume{ID: "v1", Name: "scratch", PoolID: data.ID, Size: 8 * gib}).Error)

	got, err := PoolAllocations(context.Background(), db, "node-1")
	require.NoError(t, err)
	// data: vm-a 100 + vm-c 50 + vm-d 20 + extra 15 + pending 5 + volume 8
	assert.Equal(t, int64(198), got[data.ID]/gib, "primary pool")
	// local: vm-b 30 + vm-a's extra disk that was created on the root filesystem
	assert.Equal(t, int64(40), got[local.ID]/gib, "default-directory pool")

	all, err := PoolAllocations(context.Background(), db, "")
	require.NoError(t, err)
	assert.Equal(t, got[data.ID], all[data.ID], "every node at once gives the same per-pool figure")
}

func TestAdmitPoolDisk(t *testing.T) {
	ctx := context.Background()
	db, data, _ := placementDB(t)
	// 1500 GB already promised on a 900 GiB pool with 600 GiB really free.
	require.NoError(t, db.Exec(`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-a', 'node-1', '{"cpu":1,"ram":1024,"disk":1500}', 'pool-data')`).Error)
	setRatio := func(v string) {
		require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
		if v != "" {
			require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', ?)`, v).Error)
		}
	}

	setRatio("")
	assert.NoError(t, AdmitPoolDisk(ctx, db, data, 40), "no ratio set: a pool past 1.0x keeps taking orders, as it did before")
	assert.ErrorIs(t, AdmitPoolDisk(ctx, db, data, 590), ErrNodeCapacityExceeded, "the free-space floor holds with no ratio")

	// The old key multiplied free space and was saved as 1 by the form default.
	setRatio(`{"diskOvercommitRatio": 1}`)
	assert.NoError(t, AdmitPoolDisk(ctx, db, data, 40), "the retired setting must not become a 1.0x cap")

	setRatio(`{"diskAllocationRatio": 2}`)
	assert.NoError(t, AdmitPoolDisk(ctx, db, data, 300), "1800 of 1800 GB allowed")
	err := AdmitPoolDisk(ctx, db, data, 301)
	assert.ErrorIs(t, err, ErrNodeCapacityExceeded, "one GB over the cap, with free space to spare")
	assert.ErrorIs(t, AdmitPoolDisk(ctx, db, data, 590), ErrNodeCapacityExceeded, "a ratio never lets in a disk that real free space cannot hold")

	// An extra disk is re-checked after its reservation is recorded, so two
	// attaches racing for the last room both see each other.
	require.NoError(t, db.Exec(`INSERT INTO disk_quota_reservations (id, vm_id, size_gb, status) VALUES ('r1', 'vm-a', 300, 'pending')`).Error)
	assert.NoError(t, admitPoolRatio(ctx, db, data, 0), "the reservation takes the pool exactly to its cap")
	require.NoError(t, db.Exec(`INSERT INTO disk_quota_reservations (id, vm_id, size_gb, status) VALUES ('r2', 'vm-a', 1, 'pending')`).Error)
	assert.ErrorIs(t, admitPoolRatio(ctx, db, data, 0), ErrNodeCapacityExceeded, "a second attach has pushed it over")

	assert.NoError(t, AdmitPoolDisk(ctx, db, nil, 4000), "a node with no pool known is not a full node")
	assert.NoError(t, AdmitPoolDisk(ctx, db, data, 0), "nothing added, nothing to refuse")
}

func TestVMPool(t *testing.T) {
	ctx := context.Background()
	db, data, local := placementDB(t)
	require.NoError(t, db.Exec(`INSERT INTO vms (id, node_id, storage_pool_id) VALUES ('vm-b', 'node-1', 'pool-local'), ('vm-c', 'node-1', NULL), ('vm-d', 'node-1', 'pool-elsewhere')`).Error)

	assert.Equal(t, local.ID, VMPool(ctx, db, "vm-b", "node-1").ID, "an extra disk goes where the VM's own disk is")
	assert.Equal(t, data.ID, VMPool(ctx, db, "vm-c", "node-1").ID, "nothing recorded: the node's provisioning pool")
	assert.Equal(t, data.ID, VMPool(ctx, db, "vm-d", "node-1").ID, "a pool left behind on another node is not used")
	assert.Nil(t, VMPool(ctx, db, "vm-c", "node-without-pools"))
}

// The reserve has to be big enough to act in: delete a volume, take a backup,
// let a thin disk grow. Pin it so nobody quietly trims it to nothing.
func TestHeadroomIsMeaningful(t *testing.T) {
	if diskHeadroomBytes < 10*gib {
		t.Errorf("headroom of %d bytes leaves no room to recover a full pool", diskHeadroomBytes)
	}

	// A pool with exactly the disk's size free must be rejected.
	pool := &models.StoragePool{TotalSpace: 500 * gib, AvailableSpace: 40 * gib}
	if PoolFits(pool, 40) {
		t.Error("a pool with room for the disk and nothing else must not be chosen")
	}
}
