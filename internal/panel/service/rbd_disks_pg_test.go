//go:build pg

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// CephStatus is the fake agent reaching the test's cluster and listing every
// pool it is asked about.
func (f *fakeNode) CephStatus(_ context.Context, req *pb.CephStatusRequest) (*pb.CephStatusResponse, error) {
	resp := &pb.CephStatusResponse{Success: true, Fsid: cephFSID, Health: models.CephHealthOK}
	for _, p := range req.Pools {
		resp.Pools = append(resp.Pools, &pb.CephPoolStatus{Pool: p.Pool, Namespace: p.Namespace})
	}
	return resp, nil
}

// rbdFixture is two nodes with one Ceph cluster enabled on both, and an RBD
// pool that is a member pool on each.
type rbdFixture struct {
	*migrationFixture
	cluster        string
	ref            string // the pool as the agents are told of it
	srcRow, dstRow string
}

func newRBDFixture(t *testing.T) *rbdFixture {
	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	t.Cleanup(func() { models.SetSecretKeys("") })
	f := newMigrationFixture(t)
	for _, n := range []*fakeNode{f.src, f.dst} {
		n.features = []string{models.FeatureSharedStorage, models.FeatureVMRecovery}
	}
	c := models.CephCluster{Name: "ceph-jkt", Monitors: "10.0.0.1,10.0.0.2", ClientID: "maburvm", ClientKey: cephKey, FSID: cephFSID}
	require.NoError(t, f.db.Create(&c).Error)
	p := models.SharedStoragePool{Name: "ceph-vms", Kind: models.SharedPoolRBD, Path: "vms", CephClusterID: &c.ID, RBDPool: "vms"}
	require.NoError(t, f.db.Create(&p).Error)
	r := &rbdFixture{migrationFixture: f, cluster: c.ID, ref: models.RBDPoolRef(c.ID, "vms", "")}
	for node, row := range map[string]*string{f.srcID: &r.srcRow, f.dstID: &r.dstRow} {
		require.NoError(t, f.db.Create(&models.CephClusterNode{ClusterID: c.ID, NodeID: node, Status: models.CephNodeOK}).Error)
		require.NoError(t, f.db.Raw(`INSERT INTO storage_pools (name, type, status, total_space, available_space, path, file_format, node_id, shared_pool_id)
			VALUES ('ceph-vms', 'rbd', 'online', 10995116277760, 5497558138880, 'vms', 'raw', ?, ?) RETURNING id`, node, p.ID).Scan(row).Error)
	}
	return r
}

// addRBDVM puts a VM on the source whose disks are images in the pool.
func (r *rbdFixture) addRBDVM(t *testing.T, id, status, addr string) {
	r.addVM(t, id, "ceph-"+id[:4], status, addr)
	require.NoError(t, r.db.Exec(`UPDATE vms SET storage_pool_id = ? WHERE id = ?`, r.srcRow, id).Error)
	require.NoError(t, r.db.Exec(`UPDATE vm_disks SET path = ? WHERE vm_id = ?`, r.ref+"/"+models.RBDDiskName(id, "d1"), id).Error)
	r.src.vms[id].rbd, r.src.vms[id].cache = r.ref, "none"
}

const rbdVM = "99999999-0000-0000-0000-00000000cef1"

// A VM on RBD moves between nodes that have the cluster without a copy, and
// lands on the destination's member of the same pool.
func TestRBDVMMovesWithoutACopy(t *testing.T) {
	r := newRBDFixture(t)
	ctx := context.Background()
	r.addRBDVM(t, rbdVM, "running", "198.51.100.41")
	// Its snapshots are in the images, so they do not stop the move.
	require.NoError(t, r.db.Exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'nightly', '', 'completed')`, rbdVM).Error)

	chk, err := r.svc.Check(ctx, MigrationRequest{VMID: rbdVM, DestNodeID: r.dstID})
	require.NoError(t, err)
	require.True(t, chk.CanMove, "%v", chk.Blockers)
	assert.True(t, chk.CopyFree)
	assert.Equal(t, []string{"vda", "vdb"}, chk.SharedDisks)
	assert.Equal(t, r.dstRow, chk.DestPoolID)

	row := r.queueMove(t, rbdVM)
	require.NoError(t, r.svc.RunMigrationJob(ctx, row.ID))
	got := r.migration(t, row.ID)
	require.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.Empty(t, r.dst.prepared, "no disk is made on the destination")
	assert.Equal(t, "rbd "+r.ref, r.dst.preparedPool, "the destination is told the pool with its cluster")
	for _, m := range r.src.maps {
		if m.Target != "sda" {
			assert.False(t, m.Copy, "%s is not copied", m.Target)
			assert.True(t, models.IsRBDPath(m.DestPath), "%s stays the image it is", m.Target)
		}
	}
	var moved struct{ NodeID, StoragePoolID string }
	require.NoError(t, r.db.Raw(`SELECT node_id, storage_pool_id FROM vms WHERE id = ?`, rbdVM).Scan(&moved).Error)
	assert.Equal(t, r.dstID, moved.NodeID)
	assert.Equal(t, r.dstRow, moved.StoragePoolID)
}

// Nothing is copied out of Ceph or into it by a move: a node without the pool
// online is refused, and so is an RBD pool as the destination of local disks.
func TestRBDMoveIsRefusedWhereItWouldNeedACopy(t *testing.T) {
	r := newRBDFixture(t)
	ctx := context.Background()
	r.addRBDVM(t, rbdVM, "stopped", "198.51.100.42")
	const local = "99999999-0000-0000-0000-00000000cef2"
	r.addVM(t, local, "local-vm", "stopped", "198.51.100.43")

	chk, err := r.svc.Check(ctx, MigrationRequest{VMID: local, DestNodeID: r.dstID, DestPoolID: r.dstRow})
	require.NoError(t, err)
	assert.False(t, chk.CanMove)
	assert.Contains(t, joinBlockers(chk.Blockers), "does not copy disks into one")

	// The destination loses the pool (the cluster was disabled there).
	require.NoError(t, r.db.Exec(`UPDATE storage_pools SET status = 'degraded' WHERE id = ?`, r.dstRow).Error)
	chk, err = r.svc.Check(ctx, MigrationRequest{VMID: rbdVM, DestNodeID: r.dstID})
	require.NoError(t, err)
	assert.False(t, chk.CanMove)
	assert.Contains(t, joinBlockers(chk.Blockers), "Ceph RBD pool that node beta does not have online")
}

func joinBlockers(b []string) string {
	out := ""
	for _, s := range b {
		out += s + "\n"
	}
	return out
}

// A VM on RBD is recovered from a failed node on its own disks: the new node
// is given the images by name, asked to write the setup disc again, and the
// VM's snapshots, which are in the pool, keep their records.
func TestRecoverRBDVMFromAFailedNode(t *testing.T) {
	r := newRBDFixture(t)
	f, ctx := r.migrationFixture, context.Background()
	r.addRBDVM(t, rbdVM, "running", "198.51.100.44")
	require.NoError(t, f.db.Exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'before-upgrade', '', 'completed')`, rbdVM).Error)
	f.markDown(t)

	item, opt := f.option(t, rbdVM)
	assert.Equal(t, "ceph-vms", item.SharedPool)
	assert.Equal(t, models.RecoveryShared, opt.Method)
	require.True(t, opt.CanRecover, "%v %v", opt.Blockers, opt.AddressBlockers)
	assert.NotContains(t, joinBlockers(opt.Warnings), "snapshots cannot be used")

	started, err := f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: rbdVM, DestNodeID: f.dstID}}, RequestedBy: f.user})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	done := f.recovery(t, started.Queued[0].ID)
	require.Equal(t, models.RecoveryCompleted, done.Status, done.Error)

	adopt := f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_ADOPT)
	require.NotNil(t, adopt)
	require.Len(t, adopt.AdoptDisks, 2)
	assert.Equal(t, r.ref+"/"+models.RBDDiskName(rbdVM, "vda"), adopt.AdoptDisks[0].Path)
	assert.Equal(t, r.ref+"/"+models.RBDDiskName(rbdVM, "d1"), adopt.AdoptDisks[1].Path)
	assert.Equal(t, "1", adopt.Config.Metadata[models.AdoptSeedKey], "its setup disc was on the failed node")
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE))

	var snaps, disks int64
	require.NoError(t, f.db.Model(&models.Snapshot{}).Where("vm_id = ?", rbdVM).Count(&snaps).Error)
	assert.EqualValues(t, 1, snaps, "RBD snapshots came along with the disks")
	require.NoError(t, f.db.Model(&models.VMDisk{}).Where("vm_id = ?", rbdVM).Count(&disks).Error)
	assert.EqualValues(t, 1, disks, "its data disk is still its data disk")
	var pool string
	require.NoError(t, f.db.Raw(`SELECT storage_pool_id FROM vms WHERE id = ?`, rbdVM).Scan(&pool).Error)
	assert.Equal(t, r.dstRow, pool)
}

// What a raw image on RBD cannot do is refused before anything is queued.
func TestRBDVMRefusals(t *testing.T) {
	r := newRBDFixture(t)
	f, ctx := r.migrationFixture, context.Background()
	r.addRBDVM(t, rbdVM, "running", "198.51.100.45")
	vmRepo := repository.NewVMRepository(f.db)
	snaps := &SnapshotService{db: f.db, snapshotRepo: repository.NewSnapshotRepository(f.db), vmRepo: vmRepo}
	vm, err := vmRepo.GetByID(ctx, rbdVM)
	require.NoError(t, err)
	require.True(t, VMOnRBD(ctx, f.db, vm))

	// A snapshot holds the disks, never the memory.
	_, err = snaps.CreateSnapshot(ctx, &CreateSnapshotRequest{VMID: rbdVM, Name: "with-memory", UserID: f.user})
	assert.ErrorIs(t, err, ErrSnapshotRBDMemory)

	require.NoError(t, f.db.Exec(`UPDATE vms SET status = 'stopped' WHERE id = ?`, rbdVM).Error)
	_, err = snaps.CreateSnapshot(ctx, &CreateSnapshotRequest{VMID: rbdVM, Name: "before upgrade", UserID: f.user})
	assert.ErrorIs(t, err, ErrSnapshotRBDName)

	// A disk does not move between Ceph and a node's own storage.
	_, err = f.svc.vms.StartDiskMove(ctx, rbdVM, "vda", "", f.user)
	assert.Error(t, err)
	var local string
	require.NoError(t, f.db.Raw(`SELECT id FROM storage_pools WHERE node_id = ? AND type = 'dir'`, f.srcID).Scan(&local).Error)
	_, err = f.svc.vms.StartDiskMove(ctx, rbdVM, "vda", local, f.user)
	require.ErrorIs(t, err, ErrInvalidStoragePool)
	assert.Contains(t, err.Error(), "Ceph RBD")

	// A stopped VM's password is written onto its disk as a file; an image is not one.
	require.NoError(t, f.db.Exec(`UPDATE vms SET status = 'stopped' WHERE id = ?`, rbdVM).Error)
	_, err = f.svc.vms.ResetPassword(ctx, &VMResetPasswordRequest{VMID: rbdVM, Password: "S3cure-pass-word!"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ceph RBD")

	// A VM on RBD is not cloned.
	_, err = f.svc.vms.CloneVM(ctx, &CloneVMRequest{SourceVMID: rbdVM})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ceph RBD")
}
