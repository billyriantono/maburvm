//go:build pg

package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// The fake agent's side of a recovery: it defines a VM on ADOPT, makes one on
// CREATE, and keeps the list of VMs it was told live elsewhere.
func (f *fakeNode) ExecuteVMCommand(_ context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	if hook := f.onDefine; hook != nil && req.Command != pb.VMCommandType_VM_COMMAND_TYPE_START {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, req)
	fail := func(msg string) (*pb.VMCommandResponse, error) {
		return &pb.VMCommandResponse{Message: msg, Error: &pb.ErrorResponse{Message: msg}}, nil
	}
	for _, id := range f.fenced {
		if id == req.VmId {
			return fail("this VM was recovered onto another node")
		}
	}
	switch req.Command {
	case pb.VMCommandType_VM_COMMAND_TYPE_ADOPT:
		f.calls = append(f.calls, "adopt")
		if f.failAdopt != "" {
			return fail(f.failAdopt)
		}
		f.vms[req.VmId] = &fakeVM{state: "shutoff", pool: path.Dir(req.AdoptDisks[0].Path), cache: "none"}
		if p := req.AdoptDisks[0].Path; models.IsRBDPath(p) {
			f.vms[req.VmId].rbd = p[:strings.LastIndex(p, "/")]
		}
	case pb.VMCommandType_VM_COMMAND_TYPE_CREATE:
		f.calls = append(f.calls, "create")
		f.vms[req.VmId] = &fakeVM{state: "running", pool: req.PoolPath}
	case pb.VMCommandType_VM_COMMAND_TYPE_START:
		f.calls = append(f.calls, "start")
		if vm := f.vms[req.VmId]; vm != nil {
			vm.state = "running"
		} else {
			return fail("domain not found")
		}
	default:
		return fail("unexpected command")
	}
	return &pb.VMCommandResponse{Success: true}, nil
}

func (f *fakeNode) SetFencedVMs(_ context.Context, req *pb.SetFencedVMsRequest) (*pb.SetFencedVMsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fenced = req.VmIds
	resp := &pb.SetFencedVMsResponse{Success: true}
	for _, id := range req.VmIds {
		v := &pb.FencedVM{VmId: id}
		if vm := f.vms[id]; vm != nil {
			v.Defined, v.State = true, vm.state
			v.Disks = []*pb.FencedDisk{{Target: "vda", Path: vm.pool + "/" + id + ".qcow2", SizeBytes: 5 << 30, Shared: f.member[vm.pool]}}
		}
		resp.Vms = append(resp.Vms, v)
	}
	return resp, nil
}

func (f *fakeNode) RemoveFencedVM(_ context.Context, req *pb.RemoveFencedVMRequest) (*pb.RemoveFencedVMResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm := f.vms[req.VmId]
	if vm != nil && vm.state != "shutoff" && !req.ForceStop {
		return &pb.RemoveFencedVMResponse{Error: &pb.ErrorResponse{Message: "the old copy is running on this node"}}, nil
	}
	f.calls = append(f.calls, "remove-leftover")
	delete(f.vms, req.VmId)
	return &pb.RemoveFencedVMResponse{Success: true, Removed: []string{"/vm/" + req.VmId + ".qcow2"}}, nil
}

func (f *fakeNode) command(kind pb.VMCommandType) *pb.VMCommandRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if c.Command == kind {
			return c
		}
	}
	return nil
}

// markDown leaves the source node as the node checks leave one that died ten
// minutes ago, and has the panel find its agent port closed.
func (f *migrationFixture) markDown(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.Exec(`UPDATE nodes SET status = 'offline', failed_polls = 10, last_report_at = NOW() - INTERVAL '10 minutes' WHERE id = ?`, f.srcID).Error)
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return false }
	f.svc.enqueueRecovery = func(context.Context, *models.VMRecovery) (int64, error) { return 0, nil }
}

func (f *migrationFixture) recovery(t *testing.T, id string) models.VMRecovery {
	t.Helper()
	var r models.VMRecovery
	require.NoError(t, f.db.Where("id = ?", id).First(&r).Error)
	return r
}

func (f *migrationFixture) option(t *testing.T, vmID string) (RecoveryPlanItem, RecoveryOption) {
	t.Helper()
	plan, err := f.svc.RecoveryPlanFor(context.Background(), f.srcID)
	require.NoError(t, err)
	for _, item := range plan.Items {
		if item.VMID == vmID {
			require.Len(t, item.Options, 1)
			return item, item.Options[0]
		}
	}
	t.Fatalf("VM %s is not in the plan", vmID)
	return RecoveryPlanItem{}, RecoveryOption{}
}

const recVM = "88888888-8888-8888-8888-888888888888"

// A VM on shared storage is recovered from a failed node onto another member:
// only once staff say the node is down and the panel has not heard from it,
// on the same disks, as the same VM. When the node comes back it is told the
// VM lives elsewhere, the VM never goes back by itself, and its old
// definition is removed without touching the disks.
func TestRecoverSharedVMFromAFailedNode(t *testing.T) {
	s := newSharedFixture(t)
	f, ctx := s.migrationFixture, context.Background()
	f.dst.features = append(f.dst.features, models.FeatureVMRecovery)
	s.addSharedVM(t, recVM, "running", "198.51.100.5", "none")
	choice := RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID}}, RequestedBy: f.user}
	f.svc.enqueueRecovery = func(context.Context, *models.VMRecovery) (int64, error) { return 0, nil }

	// The node is up: nothing is recovered, whatever the operator says.
	_, err := f.svc.StartRecovery(ctx, f.srcID, choice)
	require.ErrorIs(t, err, ErrRecoveryRefused)
	assert.Contains(t, err.Error(), "marked offline")

	// Marked offline by hand, while its agent still reports: refused.
	require.NoError(t, f.db.Exec(`UPDATE nodes SET status = 'offline', failed_polls = 0, last_report_at = NOW() WHERE id = ?`, f.srcID).Error)
	_, err = f.svc.StartRecovery(ctx, f.srcID, choice)
	require.ErrorIs(t, err, ErrRecoveryRefused)
	assert.Contains(t, err.Error(), "missed 0 checks")

	f.markDown(t)
	// Down for the checks, but its port answers this moment: refused.
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return true }
	_, err = f.svc.StartRecovery(ctx, f.srcID, choice)
	require.ErrorIs(t, err, ErrRecoveryRefused)
	assert.Contains(t, err.Error(), "answers right now")
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return false }

	// Without the operator's word that it is down: refused.
	_, err = f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{VMs: choice.VMs})
	require.ErrorIs(t, err, ErrRecoveryRefused)
	assert.Contains(t, err.Error(), "powered off")

	item, opt := f.option(t, recVM)
	assert.Equal(t, "vmstore", item.SharedPool)
	assert.Equal(t, models.RecoveryShared, opt.Method)
	assert.True(t, opt.CanRecover, "%v %v", opt.Blockers, opt.AddressBlockers)

	started, err := f.svc.StartRecovery(ctx, f.srcID, choice)
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	row := started.Queued[0]
	assert.True(t, VMMigrating(ctx, f.db, recVM), "nothing else changes a VM being recovered")
	require.ErrorIs(t, vmBusy(ctx, f.db, recVM), ErrVMMigrating)
	again, err := f.svc.StartRecovery(ctx, f.srcID, choice)
	require.NoError(t, err)
	require.Len(t, again.Refused, 1)
	assert.Contains(t, again.Refused[0].Reason, "already being recovered")

	require.NoError(t, f.svc.RunRecoveryJob(ctx, row.ID))
	done := f.recovery(t, row.ID)
	require.Equal(t, models.RecoveryCompleted, done.Status, done.Error)
	assert.True(t, done.Moved)
	assert.Equal(t, models.OldCopyPending, done.OldCopy)
	assert.False(t, VMMigrating(ctx, f.db, recVM))

	// Defined on its own disks: nothing created, nothing copied.
	adopt := f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_ADOPT)
	require.NotNil(t, adopt)
	require.Len(t, adopt.AdoptDisks, 2)
	assert.Equal(t, sharedDir+"/"+recVM+".qcow2", adopt.AdoptDisks[0].Path)
	assert.Equal(t, "vdb", adopt.AdoptDisks[1].Target)
	assert.EqualValues(t, 2, adopt.Config.Resources.Vcpus)
	assert.Equal(t, "198.51.100.5", adopt.Config.NetworkConfig.Interfaces[0].IpAddress)
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE))
	assert.True(t, f.dst.called("start"))
	assert.True(t, f.dst.called("network"))

	// The same VM, on the other node.
	var vm struct {
		NodeID, Status, StoragePoolID, UserID string
	}
	require.NoError(t, f.db.Raw(`SELECT node_id, status, storage_pool_id, user_id FROM vms WHERE id = ?`, recVM).Scan(&vm).Error)
	assert.Equal(t, f.dstID, vm.NodeID)
	assert.Equal(t, "running", vm.Status)
	assert.Equal(t, s.dstRow, vm.StoragePoolID)
	assert.Equal(t, f.user, vm.UserID)
	var onDst int64
	require.NoError(t, f.db.Raw(`SELECT (SELECT count(*) FROM ip_addresses WHERE vm_id = ? AND node_id = ? AND host(address) = '198.51.100.5')
		+ (SELECT count(*) FROM port_forwards WHERE vm_id = ? AND node_id = ?)`, recVM, f.dstID, recVM, f.dstID).Scan(&onDst).Error)
	assert.EqualValues(t, 2, onDst, "its address and port forward follow it")

	// Who confirmed the node was down, and the VM's own entry.
	var audits []models.AuditLog
	require.NoError(t, f.db.Where("action IN ?", []string{"node.recover_vms", "vm.recover"}).Order("action").Find(&audits).Error)
	require.Len(t, audits, 2)
	require.NotNil(t, audits[0].UserID)
	assert.Equal(t, f.user, *audits[0].UserID)
	assert.Equal(t, true, audits[0].Details["confirmed_node_is_down"])
	assert.Equal(t, recVM, *audits[1].ResourceID)

	// The node comes back with its copy running (libvirt started it at boot).
	// Its first answered check is what tells it which VMs live elsewhere.
	require.NoError(t, f.db.Exec(`UPDATE nodes SET status = 'active', failed_polls = 0 WHERE id = ?`, f.srcID).Error)
	f.svc.NodeReported(ctx, f.srcID)
	require.Eventually(t, func() bool { return f.recovery(t, row.ID).OldCopy == models.OldCopyRunning }, 5*time.Second, 20*time.Millisecond)
	f.src.mu.Lock()
	assert.Equal(t, []string{recVM}, f.src.fenced, "the node is told which VMs live elsewhere")
	f.src.mu.Unlock()
	left, err := f.svc.FenceNode(ctx, f.srcID)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, "running", left[0].State)
	assert.Equal(t, models.OldCopyRunning, f.recovery(t, row.ID).OldCopy)
	assert.Equal(t, "running", f.src.vms[recVM].state, "the panel stops nothing by itself")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, recVM).Scan(&node).Error)
	assert.Equal(t, f.dstID, node, "the VM never goes back by itself")

	// A copy that runs is removed only when the operator says to power it off.
	_, err = f.svc.RemoveLeftover(ctx, f.srcID, recVM, false, f.user, "", "")
	require.ErrorIs(t, err, ErrRecoveryRefused)
	f.src.vms[recVM].state = "shutoff"
	_, err = f.svc.FenceNode(ctx, f.srcID)
	require.NoError(t, err)
	assert.Equal(t, models.OldCopyFenced, f.recovery(t, row.ID).OldCopy)

	// Nor moved back onto it: its agent would refuse to run or network the VM.
	chk, err := f.svc.Check(ctx, MigrationRequest{VMID: recVM, DestNodeID: f.srcID})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(chk.Blockers, "; "), "still holds an old copy")

	// It cannot be recovered back onto a node that still holds its old copy.
	require.NoError(t, f.db.Exec(`UPDATE nodes SET status = 'offline', failed_polls = 10, last_report_at = NOW() - INTERVAL '10 minutes' WHERE id = ?`, f.dstID).Error)
	back, err := f.svc.RecoveryPlanFor(ctx, f.dstID)
	require.NoError(t, err)
	require.Len(t, back.Items, 1)
	assert.Contains(t, strings.Join(back.Items[0].Options[0].Blockers, "; "), "still holds an old copy")
	require.NoError(t, f.db.Exec(`UPDATE nodes SET status = 'active', failed_polls = 0 WHERE id = ?`, f.dstID).Error)

	_, err = f.svc.RemoveLeftover(ctx, f.srcID, recVM, false, f.user, "", "")
	require.NoError(t, err)
	assert.True(t, f.src.called("remove-leftover"))
	assert.Equal(t, models.OldCopyRemoved, f.recovery(t, row.ID).OldCopy)
	left, err = f.svc.FenceNode(ctx, f.srcID)
	require.NoError(t, err)
	assert.Empty(t, left)
	_, err = f.svc.RemoveLeftover(ctx, f.srcID, recVM, false, f.user, "", "")
	require.ErrorIs(t, err, ErrNoLeftover)
}

// A recovery the destination refuses changes nothing: the VM's records stay on
// the failed node and it can be recovered again. One that finds the node back
// before anything was defined is called off.
func TestFailedRecoveryLeavesTheVMWhereItWas(t *testing.T) {
	s := newSharedFixture(t)
	f, ctx := s.migrationFixture, context.Background()
	f.dst.features = append(f.dst.features, models.FeatureVMRecovery)
	s.addSharedVM(t, recVM, "running", "198.51.100.5", "none")
	f.markDown(t)
	choice := RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID}}}

	// The disk is open in a running VM: the node was not down after all.
	f.dst.failAdopt = "disk vda is open in a running VM"
	started, err := f.svc.StartRecovery(ctx, f.srcID, choice)
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	failed := f.recovery(t, started.Queued[0].ID)
	assert.Equal(t, models.RecoveryFailed, failed.Status)
	assert.Contains(t, failed.Error, "open in a running VM")
	assert.False(t, failed.Moved)
	assert.Empty(t, failed.OldCopy)
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, recVM).Scan(&node).Error)
	assert.Equal(t, f.srcID, node)
	assert.False(t, VMMigrating(ctx, f.db, recVM))

	// The node answers again between the request and the job: called off.
	f.dst.failAdopt = ""
	f.dst.commands = nil
	started, err = f.svc.StartRecovery(ctx, f.srcID, choice)
	require.NoError(t, err)
	require.Len(t, started.Queued, 1)
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return true }
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	failed = f.recovery(t, started.Queued[0].ID)
	assert.Equal(t, models.RecoveryFailed, failed.Status)
	assert.Contains(t, failed.Error, "answers right now")
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_ADOPT), "nothing is defined once the node is back")

	// An agent too old to adopt is not asked to.
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return false }
	f.dst.features = []string{models.FeatureSharedStorage}
	_, opt := f.option(t, recVM)
	assert.False(t, opt.CanRecover)
	assert.Contains(t, strings.Join(opt.Blockers, "; "), "agent update")
}

type fakeRestore struct{ err error }

func (r fakeRestore) RestoreSourceFor(context.Context, *models.Backup, *models.Node) (*RestoreSource, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &RestoreSource{SHA256: "abc123"}, nil
}

// A VM on the failed node's own storage is restored from its latest backup
// onto another node as the same VM; one with no backup is listed with the
// reason. The old node's copy is left alone until staff remove it.
func TestRecoverLocalVMFromItsBackup(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const bare = "99999999-9999-9999-9999-999999999999"
	f.addVM(t, recVM, "web", "running", "198.51.100.5")
	f.addVM(t, bare, "nobackup", "running", "198.51.100.6")
	require.NoError(t, f.db.Exec(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, checksum, completed_at) VALUES
		(?, 's3', 'backups/old.qcow2', 'completed', 'aaa', NOW() - INTERVAL '2 days'),
		(?, 's3', 'backups/new.qcow2', 'completed', 'abc123', NOW() - INTERVAL '1 hour'),
		(?, 's3', 'backups/broken.qcow2', 'failed', '', NOW())`, recVM, recVM, recVM).Error)
	require.NoError(t, f.db.Exec(`UPDATE vms SET hardware = '{"firmware":"uefi"}' WHERE id = ?`, recVM).Error)
	f.dst.features = []string{models.FeatureUEFI}
	f.svc.SetBackups(fakeRestore{})
	f.markDown(t)

	// An agent that cannot say whether the VM is there is not used: a restore
	// it ran could never be followed by the records.
	_, opt := f.option(t, recVM)
	assert.Contains(t, strings.Join(opt.Blockers, "; "), "agent update")
	f.dst.features = append(f.dst.features, models.FeatureVMRecovery)

	item, opt := f.option(t, bare)
	assert.Contains(t, item.Reason, "no finished backup")
	assert.False(t, opt.CanRecover)

	item, opt = f.option(t, recVM)
	require.NotNil(t, item.Backup)
	assert.Empty(t, item.SharedPool)
	assert.Equal(t, models.RecoveryBackup, opt.Method)
	require.True(t, opt.CanRecover, "%v %v", opt.Blockers, opt.AddressBlockers)
	assert.Contains(t, strings.Join(opt.Warnings, "; "), "what was written since is not in it")

	started, err := f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{
		{VMID: recVM, DestNodeID: f.dstID}, {VMID: bare, DestNodeID: f.dstID}}})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1)
	require.Len(t, started.Refused, 1)
	assert.Equal(t, bare, started.Refused[0].VMID)
	row := started.Queued[0]
	require.NotNil(t, row.BackupID)

	f.dst.onDefine = func() { assert.True(t, VMMigrating(ctx, f.db, recVM)) }
	require.NoError(t, f.svc.RunRecoveryJob(ctx, row.ID))
	done := f.recovery(t, row.ID)
	require.Equal(t, models.RecoveryCompleted, done.Status, done.Error)

	create := f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE)
	require.NotNil(t, create)
	assert.Contains(t, create.Config.ImageId, "s3://backups/new.qcow2", "the newest finished backup")
	assert.Equal(t, "abc123", create.Config.Metadata[models.ImageSHA256Key])
	assert.Equal(t, models.FirmwareUEFI, create.Config.Metadata[models.FirmwareKey], "a restore keeps the VM's firmware")
	assert.Equal(t, "/data", create.PoolPath)
	assert.Equal(t, "198.51.100.5", create.Config.NetworkConfig.Interfaces[0].IpAddress)
	assert.Empty(t, create.AdoptDisks)

	var vm struct{ NodeID, Status, StoragePoolID, Hostname string }
	require.NoError(t, f.db.Raw(`SELECT node_id, status, storage_pool_id, hostname FROM vms WHERE id = ?`, recVM).Scan(&vm).Error)
	assert.Equal(t, f.dstID, vm.NodeID)
	assert.Equal(t, "running", vm.Status)
	assert.Equal(t, f.dstPool, vm.StoragePoolID)
	assert.Equal(t, "web", vm.Hostname, "the same VM, not a new one")
	var n int64
	require.NoError(t, f.db.Table("vms").Count(&n).Error)
	assert.EqualValues(t, 2, n)
	var diskPath string
	require.NoError(t, f.db.Raw(`SELECT path FROM vm_disks WHERE vm_id = ?`, recVM).Scan(&diskPath).Error)
	assert.Equal(t, "/data/"+recVM+"-vdb.qcow2", diskPath)

	// The old copy stays on the returned node, shut off or not, until removed.
	assert.NotNil(t, f.src.vms[recVM])
	f.src.vms[recVM].state = "shutoff"
	left, err := f.svc.FenceNode(ctx, f.srcID)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, models.RecoveryBackup, left[0].Method)
	assert.NotNil(t, f.src.vms[recVM], "listing leftovers removes nothing")

	// A backup the destination cannot read fails the recovery before anything changes.
	f.svc.SetBackups(fakeRestore{err: errors.New("node beta cannot read the backup destination")})
	require.NoError(t, f.db.Exec(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, checksum, completed_at) VALUES (?, 's3', 'backups/b.qcow2', 'completed', 'c', NOW())`, bare).Error)
	started, err = f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: bare, DestNodeID: f.dstID}}})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	failed := f.recovery(t, started.Queued[0].ID)
	assert.Equal(t, models.RecoveryFailed, failed.Status)
	assert.Contains(t, failed.Error, "cannot read the backup destination")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, bare).Scan(&node).Error)
	assert.Equal(t, f.srcID, node)
}

// A VM only goes to a node its address pools reach. Where they do not, it is
// refused with the reason unless staff choose new addresses, and then its
// card keeps its record and gets an address from the destination's pool.
func TestRecoveryNeedsTheAddressPoolOrANewAddress(t *testing.T) {
	s := newSharedFixture(t)
	f, ctx := s.migrationFixture, context.Background()
	f.dst.features = append(f.dst.features, models.FeatureVMRecovery)
	s.addSharedVM(t, recVM, "stopped", "198.51.100.5", "none")
	const other = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, f.db.Exec(q, args...).Error, q)
	}
	// The VM's pool is the failed node's alone; the other node has its own.
	exec(`INSERT INTO ip_pool_nodes (pool_id, node_id) VALUES (?, ?)`, migPool, f.srcID)
	exec(`INSERT INTO ip_pools (id, name, cidr, gateway, bridge) VALUES (?, 'site-b', '203.0.113.0/24', '203.0.113.1', 'br1')`, other)
	exec(`INSERT INTO ip_pool_nodes (pool_id, node_id) VALUES (?, ?)`, other, f.dstID)
	exec(`INSERT INTO ip_addresses (pool_id, address, status) VALUES (?, '203.0.113.7', 'available')`, other)
	f.markDown(t)

	_, opt := f.option(t, recVM)
	assert.False(t, opt.CanRecover)
	assert.True(t, opt.CanWithNewAddress, "%v", opt.Blockers)
	assert.Contains(t, strings.Join(opt.AddressBlockers, "; "), "address pool public is not available on node beta")

	started, err := f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID}}})
	require.NoError(t, err)
	require.Len(t, started.Refused, 1)
	assert.Contains(t, started.Refused[0].Reason, "address pool public is not available")

	// Start left out: a VM that was stopped stays stopped.
	started, err = f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID, NewAddress: true}}})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	done := f.recovery(t, started.Queued[0].ID)
	require.Equal(t, models.RecoveryCompleted, done.Status, done.Error)
	assert.Contains(t, done.Note, "198.51.100.5 was replaced by 203.0.113.7")

	adopt := f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_ADOPT)
	require.NotNil(t, adopt)
	iface := adopt.Config.NetworkConfig.Interfaces[0]
	assert.Equal(t, "203.0.113.7", iface.IpAddress)
	assert.Equal(t, "203.0.113.1", iface.Gateway)
	assert.Equal(t, "br1", iface.BridgeName)
	assert.False(t, f.dst.called("start"), "a stopped VM is not started")

	var nets []string
	require.NoError(t, f.db.Raw(`SELECT host(ip_address) FROM networks WHERE vm_id = ?`, recVM).Scan(&nets).Error)
	assert.Equal(t, []string{"203.0.113.7"}, nets, "the card keeps its record with the new address")
	var old struct {
		Status string
		VMID   *string `gorm:"column:vm_id"`
	}
	require.NoError(t, f.db.Raw(`SELECT status, vm_id FROM ip_addresses WHERE host(address) = '198.51.100.5'`).Scan(&old).Error)
	assert.Equal(t, "available", old.Status)
	assert.Nil(t, old.VMID)
	var forwards int64
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM port_forwards WHERE vm_id = ? AND node_id = ?`, recVM, f.dstID).Scan(&forwards).Error)
	assert.EqualValues(t, 1, forwards, "its port forward stays on the card")
	var status string
	require.NoError(t, f.db.Raw(`SELECT status FROM vms WHERE id = ?`, recVM).Scan(&status).Error)
	assert.Equal(t, "stopped", status)
}

// A job that resumes after the destination was asked and the answer was lost
// starts nothing new once the failed node is back, and finishes the recovery
// when the VM turns out to be on the destination.
func TestResumedRecoveryStartsNothingOnceTheNodeIsBack(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	f.addVM(t, recVM, "web", "running", "198.51.100.5")
	require.NoError(t, f.db.Exec(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, checksum, completed_at) VALUES (?, 's3', 'backups/new.qcow2', 'completed', 'abc123', NOW())`, recVM).Error)
	f.dst.features = []string{models.FeatureVMRecovery}
	f.svc.SetBackups(fakeRestore{})
	f.markDown(t)
	started, err := f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID}}})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	id := started.Queued[0].ID
	require.NoError(t, f.db.Exec(`UPDATE vm_recoveries SET status = 'running', phase = ?, started_at = NOW() WHERE id = ?`, recRestoring, id).Error)
	f.svc.nodeAnswers = func(context.Context, *models.Node) bool { return true }

	row := f.recovery(t, id)
	require.ErrorIs(t, f.svc.recover(ctx, &row), errInterrupted, "an earlier download may still end with the VM there")
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE), "nothing new is started once the node is back")

	f.dst.mu.Lock()
	f.dst.vms[recVM] = &fakeVM{state: "running", pool: "/data"}
	f.dst.mu.Unlock()
	require.NoError(t, f.svc.RunRecoveryJob(ctx, id))
	done := f.recovery(t, id)
	require.Equal(t, models.RecoveryCompleted, done.Status, done.Error)
	assert.True(t, done.Moved)
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE))
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, recVM).Scan(&node).Error)
	assert.Equal(t, f.dstID, node)
}

// What the destination holds already is not built on: a definition of the VM
// left there by an earlier move is not taken for the recovered VM, and a
// backup is not restored into the shared pool the VM's own disks are in.
func TestRecoveryLeavesWhatIsOnTheDestinationAlone(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	f.addVM(t, recVM, "web", "running", "198.51.100.5")
	require.NoError(t, f.db.Exec(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, checksum, completed_at) VALUES (?, 's3', 'backups/new.qcow2', 'completed', 'abc123', NOW())`, recVM).Error)
	f.dst.features = []string{models.FeatureVMRecovery}
	f.svc.SetBackups(fakeRestore{})
	f.markDown(t)
	f.dst.vms[recVM] = &fakeVM{state: "shutoff", pool: "/data"}

	started, err := f.svc.StartRecovery(ctx, f.srcID, RecoveryRequest{NodeIsDown: true, VMs: []RecoveryChoice{{VMID: recVM, DestNodeID: f.dstID}}})
	require.NoError(t, err)
	require.Len(t, started.Queued, 1, "%v", started.Refused)
	require.NoError(t, f.svc.RunRecoveryJob(ctx, started.Queued[0].ID))
	failed := f.recovery(t, started.Queued[0].ID)
	assert.Equal(t, models.RecoveryFailed, failed.Status)
	assert.Contains(t, failed.Error, "already has a definition")
	assert.Nil(t, f.dst.command(pb.VMCommandType_VM_COMMAND_TYPE_CREATE), "nothing is created over it")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, recVM).Scan(&node).Error)
	assert.Equal(t, f.srcID, node)
}

func TestBackupRecoveryIsNotRestoredOverTheVMsSharedDisk(t *testing.T) {
	s := newSharedFixture(t)
	f := s.migrationFixture
	f.dst.features = append(f.dst.features, models.FeatureVMRecovery)
	s.addSharedVM(t, recVM, "running", "198.51.100.5", "none")
	// Its system disk is on shared storage, its data disk on the node's own.
	require.NoError(t, f.db.Exec(`UPDATE vm_disks SET path = ? WHERE vm_id = ?`, "/vm/"+recVM+"-vdb.qcow2", recVM).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, checksum, completed_at) VALUES (?, 's3', 'backups/new.qcow2', 'completed', 'abc123', NOW())`, recVM).Error)
	// The destination puts new disks in its member of that shared pool.
	require.NoError(t, f.db.Exec(`UPDATE storage_pools SET is_primary = (id = ?) WHERE node_id = ?`, s.dstRow, f.dstID).Error)
	f.svc.SetBackups(fakeRestore{})
	f.markDown(t)

	_, opt := f.option(t, recVM)
	assert.Equal(t, models.RecoveryBackup, opt.Method)
	assert.False(t, opt.CanRecover)
	assert.Contains(t, strings.Join(opt.Blockers, "; "), "where the VM's disks are")
}
