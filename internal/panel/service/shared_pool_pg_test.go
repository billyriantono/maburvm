//go:build pg

package service

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

const sharedDir = "/mnt/vmstore"

// sharedFixture is the migration fixture with a shared pool both nodes mount
// at sharedDir, and a VM on the source whose disks are in it.
type sharedFixture struct {
	*migrationFixture
	pool           *models.SharedStoragePool
	store          map[string]string // the shared storage's files
	srcRow, dstRow string            // each node's member pool
}

func newSharedFixture(t *testing.T) *sharedFixture {
	f := newMigrationFixture(t)
	ctx := context.Background()
	store := map[string]string{}
	for _, n := range []*fakeNode{f.src, f.dst} {
		n.features = []string{models.FeatureSharedStorage}
		n.shared = map[string]map[string]string{sharedDir: store}
	}
	p, err := f.svc.shared.Create(ctx, models.SharedStoragePool{Name: "vmstore", Kind: models.SharedPoolMounted, Path: sharedDir})
	require.NoError(t, err)
	require.NoError(t, f.svc.shared.AddMember(ctx, p.ID, f.srcID))
	require.NoError(t, f.svc.shared.AddMember(ctx, p.ID, f.dstID))
	s := &sharedFixture{migrationFixture: f, pool: p, store: store}
	require.NoError(t, f.db.Raw(`SELECT id FROM storage_pools WHERE shared_pool_id = ? AND node_id = ?`, p.ID, f.srcID).Scan(&s.srcRow).Error)
	require.NoError(t, f.db.Raw(`SELECT id FROM storage_pools WHERE shared_pool_id = ? AND node_id = ?`, p.ID, f.dstID).Scan(&s.dstRow).Error)
	require.NotEmpty(t, s.srcRow)
	require.NotEmpty(t, s.dstRow)
	// As the node's storage report would leave them.
	require.NoError(t, f.db.Exec(`UPDATE storage_pools SET total_space = 1099511627776, available_space = 549755813888 WHERE shared_pool_id = ?`, p.ID).Error)
	return s
}

// addSharedVM puts a VM on the source with its disks in the shared pool.
func (s *sharedFixture) addSharedVM(t *testing.T, id, status, addr, cache string) {
	s.addVM(t, id, "shared-"+id[:4], status, addr)
	require.NoError(t, s.db.Exec(`UPDATE vms SET storage_pool_id = ? WHERE id = ?`, s.srcRow, id).Error)
	require.NoError(t, s.db.Exec(`UPDATE vm_disks SET path = ? WHERE vm_id = ?`, sharedDir+"/"+id+"-vdb.qcow2", id).Error)
	s.src.vms[id].pool, s.src.vms[id].cache = sharedDir, cache
}

func TestSharedPoolJoinProvesTheSameStorage(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	shared := f.svc.shared
	// alpha and beta each have a directory at the path, but not the same one.
	f.src.shared = map[string]map[string]string{sharedDir: {}}
	f.dst.shared = map[string]map[string]string{sharedDir: {}}
	p, err := shared.Create(ctx, models.SharedStoragePool{Name: "vmstore", Kind: models.SharedPoolMounted, Path: sharedDir})
	require.NoError(t, err)

	require.NoError(t, shared.AddMember(ctx, p.ID, f.srcID))
	assert.Equal(t, p.ID, f.src.shared[sharedDir][models.SharedPoolMarker], "the first member names the storage")

	err = shared.AddMember(ctx, p.ID, f.dstID)
	require.ErrorIs(t, err, ErrSharedPool)
	assert.Contains(t, err.Error(), "marker file is missing", "a node on other storage is refused with the reason")
	assert.True(t, f.dst.called("leave"), "and taken out again")
	var members int64
	require.NoError(t, f.db.Model(&models.StoragePool{}).Where("shared_pool_id = ?", p.ID).Count(&members).Error)
	assert.EqualValues(t, 1, members)

	// The same marker copied onto other storage is still not the same storage.
	f.dst.shared[sharedDir][models.SharedPoolMarker] = p.ID
	err = shared.AddMember(ctx, p.ID, f.dstID)
	require.ErrorIs(t, err, ErrSharedPool)
	assert.Contains(t, err.Error(), "not looking at the same storage")

	// Mounted for real, it joins.
	f.dst.shared[sharedDir] = f.src.shared[sharedDir]
	require.NoError(t, shared.AddMember(ctx, p.ID, f.dstID))
	for name := range f.src.shared[sharedDir] {
		assert.Equal(t, models.SharedPoolMarker, name, "the token files are removed")
	}
}

func TestSharedPoolNodesJoinOneAtATime(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	// alpha and beta are on different storage and both asked to join an empty
	// pool at once: only one may be its unchecked first member.
	f.src.shared = map[string]map[string]string{sharedDir: {}}
	f.dst.shared = map[string]map[string]string{sharedDir: {}}
	p, err := f.svc.shared.Create(ctx, models.SharedStoragePool{Name: "vmstore", Kind: models.SharedPoolMounted, Path: sharedDir})
	require.NoError(t, err)
	beta := make(chan error, 1)
	f.src.onJoin = func() {
		go func() { beta <- f.svc.shared.AddMember(ctx, p.ID, f.dstID) }()
		time.Sleep(200 * time.Millisecond) // beta joins now, unless it waits its turn
	}
	require.NoError(t, f.svc.shared.AddMember(ctx, p.ID, f.srcID))
	err = <-beta
	require.ErrorIs(t, err, ErrSharedPool)
	assert.Contains(t, err.Error(), "marker file is missing", "beta is checked against alpha")
	var members int64
	require.NoError(t, f.db.Model(&models.StoragePool{}).Where("shared_pool_id = ?", p.ID).Count(&members).Error)
	assert.EqualValues(t, 1, members)
}

func TestSharedPoolCountsOnceAndGuardsItsMembers(t *testing.T) {
	s := newSharedFixture(t)
	ctx := context.Background()
	const vm = "99999999-0000-0000-0000-000000000001"
	s.addSharedVM(t, vm, "running", "198.51.100.31", "none")

	// 20 GB of system disk and 10 GB of data disk, promised once on the pool
	// and seen from either member.
	for _, node := range []string{"", s.srcID, s.dstID} {
		alloc, err := PoolAllocations(ctx, s.db, node)
		require.NoError(t, err)
		if node != s.dstID {
			assert.EqualValues(t, 30<<30, alloc[s.srcRow], "node %q", node)
		}
		if node != s.srcID {
			assert.EqualValues(t, 30<<30, alloc[s.dstRow], "node %q", node)
		}
	}
	views, err := s.svc.shared.List(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Len(t, views[0].Members, 2)
	assert.EqualValues(t, 1099511627776, views[0].TotalSpace, "capacity is the storage's, not the members' added up")
	require.NotNil(t, views[0].AllocatedSpace)
	assert.EqualValues(t, 30<<30, *views[0].AllocatedSpace)

	// A shared pool's id names the node's member when staff choose a pool.
	got, err := chosenPool(ctx, s.db, s.pool.ID, s.dstID)
	require.NoError(t, err)
	assert.Equal(t, s.dstRow, got.ID)

	// A member leaves through the shared pool, and not while a VM is on it.
	err = NewStorageService(repository.NewStorageRepository(s.db)).DeletePool(s.srcRow)
	require.ErrorIs(t, err, ErrInvalidStoragePool)
	err = s.svc.shared.RemoveMember(ctx, s.pool.ID, s.srcID)
	require.ErrorIs(t, err, ErrSharedPool)
	assert.Contains(t, err.Error(), "1 VMs on node alpha")
	require.ErrorIs(t, s.svc.shared.Delete(ctx, s.pool.ID), ErrSharedPool)
	require.NoError(t, s.svc.shared.RemoveMember(ctx, s.pool.ID, s.dstID))
	assert.True(t, s.dst.called("leave"))

	// The other member's VM disk is not an orphan to a node that cannot see
	// the VM, and neither is a file nobody can name the owner of.
	require.NoError(t, s.svc.shared.AddMember(ctx, s.pool.ID, s.dstID))
	const gone = "99999999-0000-0000-0000-000000000002"
	require.NoError(t, s.db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status, deleted_at)
		VALUES (?, ?, ?, 'gone', ?, '{"cpu":1,"ram":512,"disk":10}', 'stopped', NOW())`, gone, s.user, s.srcID, s.tmpl).Error)
	s.dst.orphans = []*pb.OrphanDisk{
		{Path: sharedDir + "/" + vm + ".qcow2", Kind: "file", Reason: "VM system disk", VmId: vm, Maburvm: true},
		{Path: sharedDir + "/someone-elses.qcow2", Kind: "file", Reason: "disk image"},
		{Path: sharedDir + "/" + gone + ".qcow2", Kind: "file", Reason: "VM system disk", VmId: gone, Maburvm: true},
	}
	orphans, err := s.svc.vms.ListOrphanDisks(ctx, s.dstID)
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	assert.Equal(t, sharedDir+"/"+gone+".qcow2", orphans[0].Path, "only the deleted VM's disk is offered")
}

func TestLiveMoveOnSharedStorageCopiesNoDisk(t *testing.T) {
	s := newSharedFixture(t)
	ctx := context.Background()
	const vm = "99999999-0000-0000-0000-000000000003"
	s.addSharedVM(t, vm, "running", "198.51.100.33", "none")

	chk, err := s.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: s.dstID})
	require.NoError(t, err)
	require.True(t, chk.CanMove, "%v", chk.Blockers)
	assert.True(t, chk.CopyFree)
	assert.Equal(t, []string{"vda", "vdb"}, chk.SharedDisks)
	assert.Equal(t, s.dstRow, chk.DestPoolID, "the VM lands on the destination's member of the same pool")
	assert.Zero(t, chk.CopyBytes)

	row := s.queueMove(t, vm)
	require.NoError(t, s.svc.RunMigrationJob(ctx, row.ID))
	got := s.migration(t, row.ID)
	require.Equal(t, models.MigrationCompleted, got.Status, got.Error)

	assert.Empty(t, s.dst.prepared, "nothing is made on the destination for disks that are already there")
	require.Len(t, s.src.maps, 3)
	for _, m := range s.src.maps {
		assert.False(t, m.Copy, "%s is not copied", m.Target)
		assert.Equal(t, sharedDir, path.Dir(m.DestPath), "%s stays at its path", m.Target)
	}
	want := []string{sharedDir + "/" + vm + ".qcow2", sharedDir + "/" + vm + "-vdb.qcow2", sharedDir + "/" + vm + "-seed.iso"}
	assert.Equal(t, want, s.src.kept, "the source is told to keep the files the VM now runs on")

	var moved struct{ NodeID, StoragePoolID string }
	require.NoError(t, s.db.Raw(`SELECT node_id, storage_pool_id FROM vms WHERE id = ?`, vm).Scan(&moved).Error)
	assert.Equal(t, s.dstID, moved.NodeID)
	assert.Equal(t, s.dstRow, moved.StoragePoolID)
	var diskPath string
	require.NoError(t, s.db.Raw(`SELECT path FROM vm_disks WHERE vm_id = ?`, vm).Scan(&diskPath).Error)
	assert.Equal(t, sharedDir+"/"+vm+"-vdb.qcow2", diskPath)
}

func TestSharedMoveRefusedWhenTheDestinationLostItsMount(t *testing.T) {
	s := newSharedFixture(t)
	ctx := context.Background()
	const vm = "99999999-0000-0000-0000-000000000004"
	s.addSharedVM(t, vm, "running", "198.51.100.34", "none")

	var beta models.Node
	require.NoError(t, s.db.Where("id = ?", s.dstID).First(&beta).Error)
	assert.Empty(t, s.svc.shared.MemberProblem(ctx, s.pool.ID, &beta), "mounted, the periodic check passes")

	// Unmounted on beta: the path is an empty local directory again.
	s.dst.shared[sharedDir] = map[string]string{}
	assert.Contains(t, s.svc.shared.MemberProblem(ctx, s.pool.ID, &beta), "marker file is missing", "the periodic check sees the mount is gone")
	chk, err := s.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: s.dstID})
	require.NoError(t, err)
	require.False(t, chk.CanMove)
	assert.Contains(t, chk.Blockers[0], "marker file is missing")
	var st string
	require.NoError(t, s.db.Raw(`SELECT status FROM storage_pools WHERE id = ?`, s.dstRow).Scan(&st).Error)
	assert.Equal(t, "degraded", st, "the member is marked unusable")
	assert.False(t, s.src.called("migrate"))
}

func TestSharedMoveNeedsTheBackingImageOnTheSharedStorage(t *testing.T) {
	s := newSharedFixture(t)
	ctx := context.Background()
	const vm = "99999999-0000-0000-0000-000000000007"
	s.addSharedVM(t, vm, "running", "198.51.100.37", "none")

	// Built on a node's own template copy: the destination's file at that path
	// is another file, or none.
	s.src.vms[vm].backing = "/var/lib/libvirt/templates/debian-12.qcow2"
	chk, err := s.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: s.dstID})
	require.NoError(t, err)
	require.False(t, chk.CanMove)
	assert.Contains(t, strings.Join(chk.Blockers, "; "), "not in shared pool vmstore")

	// Built on an image beside it in the pool: the same file on both nodes.
	s.src.vms[vm].backing = "base.qcow2"
	chk, err = s.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: s.dstID})
	require.NoError(t, err)
	assert.True(t, chk.CanMove, "%v", chk.Blockers)
}

func TestSharedMoveLiveNeedsCacheNoneAndOfflineNeedsNoCopy(t *testing.T) {
	s := newSharedFixture(t)
	ctx := context.Background()
	const live = "99999999-0000-0000-0000-000000000005"
	s.addSharedVM(t, live, "running", "198.51.100.35", "writeback")
	chk, err := s.svc.Check(ctx, MigrationRequest{VMID: live, DestNodeID: s.dstID})
	require.NoError(t, err)
	require.False(t, chk.CanMove)
	assert.Contains(t, chk.Blockers[0], "goes through the host's cache")

	// A node whose agent would delete the shared files is never used.
	const off = "99999999-0000-0000-0000-000000000006"
	s.addSharedVM(t, off, "stopped", "198.51.100.36", "")
	s.src.features = nil
	chk, err = s.svc.Check(ctx, MigrationRequest{VMID: off, DestNodeID: s.dstID})
	require.NoError(t, err)
	require.False(t, chk.CanMove)
	assert.Contains(t, chk.Blockers[0], "needs an agent update")
	s.src.features = []string{models.FeatureSharedStorage}

	row := s.queueMove(t, off)
	assert.Equal(t, models.MigrationOffline, row.Mode)
	require.NoError(t, s.svc.RunMigrationJob(ctx, row.ID))
	got := s.migration(t, row.ID)
	require.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	require.Len(t, s.dst.maps, 3)
	for _, m := range s.dst.maps {
		assert.False(t, m.Copy, "%s is defined where it is, not copied", m.Target)
	}
	assert.Len(t, s.src.kept, 3)
}
