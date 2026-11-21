package service

import (
	"context"
	"log/slog"
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

func TestNodeLoadFits(t *testing.T) {
	n := nodeLoad{Node: models.Node{MemoryTotalBytes: 16 * gib, CPUCount: 8}, AllocatedRAMMB: 12 * 1024, AllocatedVCPU: 14}
	assert.True(t, n.ramFits(8*1024), "no RAM ratio: anything fits, as before")
	assert.True(t, n.vcpuFits(64), "no vCPU ratio: anything fits, as before")

	n.RAMRatio, n.VCPURatio = 1.0, 2.0
	assert.True(t, n.ramFits(4*1024), "exactly at the cap")
	assert.False(t, n.ramFits(4*1024+1), "one MB over")
	assert.True(t, n.vcpuFits(2), "16 of 16 vCPUs")
	assert.False(t, n.vcpuFits(3), "17 of 16 vCPUs")
	assert.True(t, n.vcpuFits(0), "a resize that adds no vCPUs is never refused")

	unmeasured := nodeLoad{RAMRatio: 1, VCPURatio: 1, AllocatedRAMMB: 1 << 30, AllocatedVCPU: 1000}
	assert.True(t, unmeasured.ramFits(1024) && unmeasured.vcpuFits(4), "a node never measured is not a full node")
}

func TestRankNodes(t *testing.T) {
	load := func(id, name string, totalGiB, allocMiB int64) nodeLoad {
		return nodeLoad{Node: models.Node{ID: id, Name: name, MemoryTotalBytes: totalGiB * gib}, AllocatedRAMMB: allocMiB}
	}
	small := load("id-small", "small", 64, 32*1024) // half full
	big := load("id-big", "big", 128, 32*1024)      // a quarter full
	twin := load("id-twin", "a-twin", 128, 32*1024) // the same as big
	fresh := load("id-fresh", "fresh", 0, 0)        // never measured
	busy := load("id-busy", "busy", 128, 120*1024)

	got := rankNodes([]nodeLoad{fresh, small, busy, big, twin}, 4096)
	var order []string
	for _, n := range got {
		order = append(order, n.Node.Name)
	}
	assert.Equal(t, []string{"a-twin", "big", "small", "busy", "fresh"}, order,
		"least allocated first, a tie by name, unmeasured last")

	// Proportional, not absolute: a big node with more free RAM but a higher
	// share allocated loses to a small node with a lower share.
	bigBusy := load("id-bb", "big-busy", 256, 192*1024)  // 64 GiB free, 75% allocated
	smallIdle := load("id-si", "small-idle", 32, 8*1024) // 24 GiB free, 25% allocated
	assert.Equal(t, "small-idle", rankNodes([]nodeLoad{bigBusy, smallIdle}, 2048)[0].Node.Name)

	again := rankNodes([]nodeLoad{twin, big, small}, 4096)
	assert.Equal(t, got[0].Node.ID, again[0].Node.ID, "the same state always picks the same node")
}

// placeDB is placementDB with the node columns placement reads.
func placeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, _, _ := placementDB(t)
	for _, stmt := range []string{
		`ALTER TABLE nodes ADD COLUMN creation_paused BOOLEAN NOT NULL DEFAULT 0`,
		`ALTER TABLE nodes ADD COLUMN failed_polls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE nodes ADD COLUMN cpu_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE nodes ADD COLUMN vcpu_ratio REAL NOT NULL DEFAULT 0`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	return db
}

func addNode(t *testing.T, db *gorm.DB, id string, cpus int, memGiB int64, extra string) models.Node {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, status, token, cpu_count, memory_total_bytes) VALUES (?, ?, '10.0.0.1', 'active', ?, ?, ?)`,
		id, id, "tok-"+id, cpus, memGiB*gib).Error)
	if extra != "" {
		require.NoError(t, db.Exec(`UPDATE nodes SET `+extra+` WHERE id = ?`, id).Error)
	}
	var n models.Node
	require.NoError(t, db.Where("id = ?", id).First(&n).Error)
	return n
}

func setQuotas(t *testing.T, db *gorm.DB, data string) {
	t.Helper()
	require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
	if data != "" {
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', ?)`, data).Error)
	}
}

func TestPlaceVM(t *testing.T) {
	ctx := context.Background()
	db := placeDB(t)
	s := &VMService{db: db, logger: slog.Default()}
	n1 := addNode(t, db, "n1", 8, 16, "")
	n2 := addNode(t, db, "n2", 8, 64, "")
	paused := addNode(t, db, "paused", 64, 512, "creation_paused = 1")
	silent := addNode(t, db, "silent", 64, 512, "failed_polls = 2")
	require.NoError(t, db.Exec(`INSERT INTO vms (id, node_id, resources) VALUES
		('a', 'n1', '{"cpu":6,"ram":2048,"disk":10}'),
		('b', 'n2', '{"cpu":2,"ram":40960,"disk":10}'),
		('gone', 'n1', '{"cpu":64,"ram":999999,"disk":10}')`).Error)
	require.NoError(t, db.Exec(`UPDATE vms SET deleted_at = '2026-01-01' WHERE id = 'gone'`).Error)
	nodes := []models.Node{n2, paused, silent, n1}
	vm := models.Resources{CPU: 4, RAM: 4096, Disk: 20}

	setQuotas(t, db, "")
	id, err := s.placeVM(ctx, nodes, vm, true)
	require.NoError(t, err)
	assert.Equal(t, "n1", id, "the least allocated node; paused and silent nodes never, a deleted VM holds nothing")
	again, _ := s.placeVM(ctx, nodes, vm, true)
	assert.Equal(t, id, again, "no round-robin: the same state, the same node")

	setQuotas(t, db, `{"vcpuAllocationRatio": 1}`)
	id, err = s.placeVM(ctx, nodes, vm, true)
	require.NoError(t, err)
	assert.Equal(t, "n2", id, "n1 would carry 10 vCPUs on 8 CPUs")

	require.NoError(t, db.Exec(`UPDATE nodes SET vcpu_ratio = 2 WHERE id = 'n1'`).Error)
	n1.VCPURatio = 2
	nodes[3] = n1
	id, _ = s.placeVM(ctx, nodes, vm, true)
	assert.Equal(t, "n1", id, "the node's own ratio wins over the panel-wide one")

	setQuotas(t, db, `{"ramOvercommitRatio": 1}`)
	id, err = s.placeVM(ctx, nodes, models.Resources{CPU: 1, RAM: 15 * 1024, Disk: 20}, true)
	require.NoError(t, err)
	assert.Equal(t, "n2", id, "RAM is checked before the node is chosen: 17 GB on a 16 GB node does not fit")

	huge := models.Resources{CPU: 1, RAM: 100 * 1024, Disk: 20}
	_, err = s.placeVM(ctx, nodes, huge, true)
	assert.ErrorIs(t, err, ErrNoAvailableNodes, "a region order fails when nothing fits")
	id, err = s.placeVM(ctx, nodes, huge, false)
	require.NoError(t, err)
	assert.Contains(t, []string{"n1", "n2"}, id, "otherwise the best node is tried, and admission says why it is refused")

	_, err = s.placeVM(ctx, []models.Node{paused}, vm, false)
	assert.ErrorIs(t, err, ErrNoAvailableNodes)

	// The node with the disk's pool full is skipped when another has room.
	require.NoError(t, db.Exec(`UPDATE storage_pools SET available_space = ? WHERE node_id = 'node-1'`, 5*gib).Error)
	require.NoError(t, db.Exec(`UPDATE storage_pools SET node_id = 'n1' WHERE node_id = 'node-1'`).Error)
	setQuotas(t, db, "")
	id, _ = s.placeVM(ctx, nodes, vm, true)
	assert.Equal(t, "n2", id, "n1's pool cannot hold the disk")
}

func TestAdmitNodeVCPU(t *testing.T) {
	ctx := context.Background()
	db := placeDB(t)
	addNode(t, db, "n1", 4, 16, "")
	addNode(t, db, "unmeasured", 0, 16, "")
	require.NoError(t, db.Exec(`INSERT INTO vms (id, node_id, resources) VALUES ('a', 'n1', '{"cpu":6,"ram":1024,"disk":10}')`).Error)

	setQuotas(t, db, "")
	assert.NoError(t, AdmitNodeVCPU(ctx, db, "n1", 100), "no ratio: no cap, as before")

	setQuotas(t, db, `{"vcpuAllocationRatio": 2}`)
	assert.NoError(t, AdmitNodeVCPU(ctx, db, "n1", 2), "8 of 8")
	err := AdmitNodeVCPU(ctx, db, "n1", 3)
	assert.ErrorIs(t, err, ErrNodeCapacityExceeded)
	assert.Contains(t, err.Error(), "9 vCPUs, over its limit of 8")
	assert.NoError(t, AdmitNodeVCPU(ctx, db, "n1", -2), "a shrink is never refused")
	assert.NoError(t, AdmitNodeVCPU(ctx, db, "unmeasured", 50), "CPUs never reported: not a full node")

	require.NoError(t, db.Exec(`UPDATE nodes SET vcpu_ratio = 3 WHERE id = 'n1'`).Error)
	assert.NoError(t, AdmitNodeVCPU(ctx, db, "n1", 6), "the node's own ratio: 12 of 12")

	alloc, err := NodeAllocationOf(ctx, db, addNode(t, db, "n3", 8, 32, "vcpu_ratio = 1.5"))
	require.NoError(t, err)
	assert.Equal(t, 12, alloc.VCPULimit)
	assert.Equal(t, 1.5, alloc.VCPURatioNode)
	assert.Equal(t, int64(32*1024), alloc.RAMTotalMB)
	assert.Zero(t, alloc.RAMLimitMB, "no RAM ratio, no RAM limit")
}

func TestLVMDisksCountInTheirPool(t *testing.T) {
	ctx := context.Background()
	db, data, _ := placementDB(t)
	thin := &models.StoragePool{ID: "pool-thin", Name: "thin", NodeID: "node-1", Path: "vg0/thin", Type: models.PoolTypeLVMThin, Status: "online", TotalSpace: 500 * gib, AvailableSpace: 400 * gib}
	thick := &models.StoragePool{ID: "pool-thick", Name: "thick", NodeID: "node-1", Path: "vg0", Type: models.PoolTypeLVM, Status: "online", TotalSpace: 800 * gib, AvailableSpace: 300 * gib}
	require.NoError(t, db.Create(thin).Error)
	require.NoError(t, db.Create(thick).Error)
	for _, stmt := range []string{
		`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-l', 'node-1', '{"cpu":1,"ram":1024,"disk":40}', 'pool-thin')`,
		`INSERT INTO vms (id, node_id, resources, storage_pool_id) VALUES ('vm-f', 'node-1', '{"cpu":1,"ram":1024,"disk":10}', 'pool-data')`,
		// In the VM's own thin pool, though the thick pool shares its volume group.
		`INSERT INTO vm_disks (id, vm_id, size_gb, path) VALUES ('d1', 'vm-l', 20, '/dev/vg0/maburvm-vm-l-d1')`,
		// A file VM's imported volume counts in the pool holding its volume group.
		`INSERT INTO vm_disks (id, vm_id, size_gb, path) VALUES ('d2', 'vm-f', 7, '/dev/vg0/imported-disk1')`,
		// A device in no registered pool stays with the VM's own pool.
		`INSERT INTO vm_disks (id, vm_id, size_gb, path) VALUES ('d3', 'vm-f', 3, '/dev/other/x')`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	got, err := PoolAllocations(ctx, db, "node-1")
	require.NoError(t, err)
	assert.Equal(t, int64(60), got[thin.ID]/gib)
	assert.Equal(t, int64(7), got[thick.ID]/gib)
	assert.Equal(t, int64(13), got[data.ID]/gib)

	assert.True(t, PoolHoldsDisk(*thin, "/dev/vg0/x"))
	assert.False(t, PoolHoldsDisk(*thin, "/dev/vg1/x"))
	assert.False(t, PoolHoldsDisk(*data, "/dev/data/x"), "a directory pool never holds a device")
	assert.True(t, PoolHoldsDisk(*data, "/data/x.qcow2"))
	assert.False(t, PoolHoldsDisk(*data, "/data/sub/x.qcow2"))

	assert.Equal(t, thin.ID, DiskPool(ctx, db, "vm-l", "node-1", "/dev/vg0/maburvm-vm-l-d1").ID, "the VM's own pool first")
	assert.Nil(t, DiskPool(ctx, db, "vm-f", "node-1", "/dev/other/x"))

	vmL := &models.VM{ID: "vm-l", NodeID: "node-1"}
	vmF := &models.VM{ID: "vm-f", NodeID: "node-1"}
	assert.True(t, VMOnBlockStorage(ctx, db, vmL), "system disk in an LVM pool")
	assert.True(t, VMOnBlockStorage(ctx, db, vmF), "a data disk on a device")
	require.NoError(t, db.Exec(`DELETE FROM vm_disks WHERE vm_id = 'vm-f'`).Error)
	assert.False(t, VMOnBlockStorage(ctx, db, vmF))
}
