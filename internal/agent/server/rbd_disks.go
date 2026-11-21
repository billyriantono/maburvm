package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/agent/ceph"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// VM disks on RBD. An image is never edited from the node: libguestfs works
// on files, so a new or reinstalled disk is prepared as a local file in the
// scratch directory and imported. What would need the disk as a file later
// (an offline password reset, a router's script) is refused with the reason.

func init() {
	libvirt.RBDScratchDir = blockScratchDir()
	ceph.StartsFile = filepath.Join(agentDataDir(), "rbd-autostart.json")
}

// rbdFreeToStart refuses to start a stopped VM while any client has one of
// its images on RBD open: that is the VM running on another node (recovered
// there, or never really gone), and two QEMUs on one image corrupt it. A
// client killed a moment ago is listed a little longer and is waited for.
func rbdFreeToStart(vmID string) error {
	if st, err := libvirt.GetVMStatus(vmID); err != nil || st != libvirt.VMStatusStopped {
		return nil // a running or paused VM holds its images itself; StartVM says the rest
	}
	disks, err := libvirt.BackupDisks(vmID)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ceph.WatcherGrace+time.Minute)
	defer cancel()
	for _, d := range disks {
		if !d.RBD {
			continue
		}
		img, err := ceph.OpenImage(d.Path)
		if err == nil {
			err = img.CheckUnused(ctx, ceph.WatcherGrace)
		}
		var open *ceph.InUseError
		if errors.As(err, &open) {
			return fmt.Errorf("the VM was not started: its disk %s (%s) is open on %s, so %w. Stop it there first",
				d.Target, open.Spec, strings.Join(open.Watchers, ", "), errRunsElsewhere)
		}
		if err != nil {
			return fmt.Errorf("the VM was not started: its disk %s could not be checked: %w", d.Target, err)
		}
	}
	return nil
}

var errRunsElsewhere = errors.New("it runs elsewhere already, and two copies would corrupt the disk")

// startRBDGuests starts, once the node has booted, the VMs on RBD that start
// with it (ceph.SetStartsWithNode), each through startVM and its checks. A
// cluster not reachable yet is tried again for a while, as QEMU would wait
// for it; a VM that runs elsewhere or was recovered elsewhere is left off.
func startRBDGuests() {
	pending := ceph.StartsAfterBoot()
	for try := 0; try < 10 && len(pending) > 0; try++ {
		if try > 0 {
			time.Sleep(time.Minute)
		}
		var again []string
		for _, id := range pending {
			st, err := libvirt.GetVMStatus(id)
			if libvirt.IsNoDomain(err) {
				_ = ceph.SetStartsWithNode(id, false)
				continue
			}
			if err == nil && st != libvirt.VMStatusStopped {
				continue
			}
			if err == nil {
				err = startVM(id)
			}
			if err == nil {
				log.Printf("[NodeAgent] VM %s on RBD started with the node", id)
				continue
			}
			log.Printf("[NodeAgent] WARNING: VM %s on RBD did not start with the node: %v", id, err)
			if !errors.Is(err, errRunsElsewhere) && !errors.Is(err, errFenced) {
				again = append(again, id)
			}
		}
		pending = again
	}
}

// requestRBDPool is the RBD pool a request places a disk in (pool type rbd,
// pool path models.RBDPoolRef); false for any other pool. The pool's cluster
// must be enabled on this node.
func requestRBDPool(poolType, poolPath string) (ref string, onRBD bool, err error) {
	if poolType != models.PoolTypeRBD {
		return "", false, nil
	}
	cluster, _, _, err := models.ParseRBDPoolRef(poolPath)
	if err != nil {
		return "", false, err
	}
	if _, err := ceph.Load(cluster); err != nil {
		return "", false, err
	}
	return poolPath, true, nil
}

// poolOfRBDDisk is the pool (models.RBDPoolRef) holding the disk at path.
func poolOfRBDDisk(path string) string { return path[:strings.LastIndex(path, "/")] }

// diskDir is the directory beside a VM's disk, where its setup disc and
// rescue disk go; fallback for a disk that is a device or an image on RBD.
func diskDir(diskPath, fallback string) string {
	if diskPath == "" || models.IsBlockDevicePath(diskPath) || ceph.IsPath(diskPath) {
		return fallback
	}
	return filepath.Dir(diskPath)
}

// cleanDiskPath is filepath.Clean for a file or device; an RBD disk path is
// kept as it is (Clean would fold the slashes of its scheme).
func cleanDiskPath(p string) string {
	if ceph.IsPath(p) {
		return p
	}
	return filepath.Clean(p)
}

// noHostEdit says why this node cannot write into the disk at path from
// outside the guest; nil when it can.
func noHostEdit(path, what string) error {
	if ceph.IsPath(path) {
		return fmt.Errorf("the VM's disk is on Ceph RBD, which the node cannot edit from outside the guest, so %s", what)
	}
	return nil
}

// scratchRoomFor refuses to prepare a disk bound for RBD from the image at
// src when the scratch directory, often on the node's root filesystem, could
// not hold the copy: filling that takes libvirt and this agent down with it.
// ponytail: the copy is budgeted at twice the image file (a compressed
// template grows when copied); read the image's allocation if that misjudges.
func scratchRoomFor(src string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return nil // the copy itself says what is wrong with the image
	}
	free, err := freeBytes(blockScratchDir())
	if err != nil {
		return nil
	}
	if need := 2 * fi.Size(); free < need+volumeReserveBytes {
		return fmt.Errorf("%s has %d GB free; preparing this disk for the Ceph pool can need %d GB there, and %d GB kept free. Free space there, or point TEMPLATE_CACHE_DIR at a larger volume",
			blockScratchDir(), free>>30, (need>>30)+1, int64(volumeReserveBytes)>>30)
	}
	return nil
}

// rbdDataDiskName names a new data disk image of the VM.
func rbdDataDiskName(vmID string) string {
	return models.RBDDiskName(vmID, "d"+strconv.FormatInt(time.Now().UnixNano(), 36))
}

// freeRBDImage names the VM's image called name in the pool, and refuses one
// that exists already: on shared storage it may be the VM's disk from another
// node, so it is never replaced.
func freeRBDImage(ctx context.Context, poolRef, name string) (ceph.Image, error) {
	img, err := ceph.PoolImage(poolRef, name)
	if err != nil {
		return img, err
	}
	if here, err := img.Exists(ctx); err != nil {
		return img, err
	} else if here {
		return img, fmt.Errorf("the RBD image %s exists already; it may be this VM's disk from another node, so it was left as it is. Remove it from the pool if it is left over from a failed create", img.Spec())
	}
	return img, nil
}

// newRBDImage makes the VM's image called name in the pool: empty at size
// bytes, or from the local image src (qcow2) and at least size bytes. The
// contents are imported under a second name and renamed into place, so a
// create that dies leaves nothing under the VM's own name.
func newRBDImage(ctx context.Context, poolRef, name, src string, size int64) (string, error) {
	img, err := freeRBDImage(ctx, poolRef, name)
	if err != nil {
		return "", err
	}
	if src == "" {
		err = img.Create(ctx, size)
	} else {
		err = img.ReplaceNew(ctx, src, "qcow2", size)
	}
	if err != nil {
		return "", err
	}
	return img.Path(), nil
}

// removeOwnedImage removes an image MaburVM made for the VM, unless a client
// still has it open. It returns why it was kept, "" when it is gone.
func removeOwnedImage(ctx context.Context, vmID, path string) string {
	img, err := ceph.OpenImage(path)
	if err != nil {
		return err.Error()
	}
	if !models.OwnsRBDName(vmID, img.Name) {
		return "not created by MaburVM for this VM; kept until an operator deletes it"
	}
	if err := img.Remove(ctx); err != nil {
		return err.Error()
	}
	log.Printf("[NodeAgent] removed RBD image %s", img.Spec())
	return ""
}

// removeVMImages removes the images a just-deleted VM had on RBD: those
// MaburVM created for it, and only when no client has them open, so a VM that
// still runs on another node keeps its disk. What stays is reported.
func removeVMImages(ctx context.Context, vmID string, disks []storage.DomainDisk) []*pb.KeptVolume {
	var kept []*pb.KeptVolume
	for _, d := range disks {
		if !d.RBD {
			continue
		}
		if why := removeOwnedImage(ctx, vmID, d.Path); why != "" {
			k := &pb.KeptVolume{Path: d.Path, Target: d.Target, Reason: why}
			if img, err := ceph.OpenImage(d.Path); err == nil {
				k.SizeBytes, _ = img.Size(ctx)
			}
			kept = append(kept, k)
		}
	}
	return kept
}

// importRestoredToRBD turns the data disks of a backup restored as a new VM,
// fetched as local files, into images of the pool. On failure nothing is left.
func importRestoredToRBD(ctx context.Context, poolRef, vmID string, files []*pb.BackupDiskFile) ([]*pb.BackupDiskFile, error) {
	defer func() {
		for _, f := range files {
			_ = os.Remove(f.GetPath())
		}
	}()
	var out []*pb.BackupDiskFile
	for _, f := range files {
		p, err := newRBDImage(ctx, poolRef, models.RBDDiskName(vmID, f.GetTarget()), f.GetPath(), 0)
		if err != nil {
			for _, d := range out {
				discardNewDisk(vmID, d.GetPath())
			}
			return nil, fmt.Errorf("disk %s: %w", f.GetTarget(), err)
		}
		out = append(out, &pb.BackupDiskFile{Target: f.GetTarget(), Path: p, VirtualSizeBytes: f.GetVirtualSizeBytes()})
	}
	return out, nil
}

// adoptRBDDisk checks a disk on RBD a VM is about to be defined on after its
// node failed, and takes it away from that node: locks are broken and whoever
// still has the image open is put on the cluster's blocklist (ceph.Image.Fence),
// so a QEMU that survives on the failed node cannot write to it any more.
func adoptRBDDisk(ctx context.Context, vmID, target, path string) error {
	img, err := ceph.OpenImage(path)
	if err != nil {
		return fmt.Errorf("disk %s: %w", target, err)
	}
	if !models.OwnsRBDName(vmID, img.Name) {
		return fmt.Errorf("disk %s (%s) is not an image MaburVM made for this VM", target, img.Spec())
	}
	if _, err := img.Size(ctx); err != nil {
		return fmt.Errorf("disk %s: %w", target, err)
	}
	fenced, err := img.Fence(ctx)
	if err != nil {
		return fmt.Errorf("disk %s: %w. Nothing was defined", target, err)
	}
	if len(fenced) > 0 {
		log.Printf("[Recovery] %s: fenced %s off %s", vmID, strings.Join(fenced, ", "), img.Spec())
	}
	return nil
}

// rbdBackupSnap is the RBD snapshot a running backup reads from. A VM
// snapshot taken from the panel is named models.RBDSnapshotPrefix + its name.
const rbdBackupSnap = "maburvm-backup"

// snapshotRun names a snapshot rollback in the registry of work on a VM's disks.
const snapshotRun = "snapshot-rollback"

func openImages(disks []storage.DomainDisk) ([]ceph.Image, error) {
	imgs := make([]ceph.Image, 0, len(disks))
	for _, d := range disks {
		img, err := ceph.OpenImage(d.Path)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", d.Target, err)
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

// snapshotOnRBD is CreateSnapshot for a VM whose disks are all on RBD. Raw
// images hold no snapshot inside them, so a VM snapshot is one RBD snapshot
// per disk under the same name: its disks, never its memory. A rollback needs
// the VM off.
func (s *NodeAgentService) snapshotOnRBD(ctx context.Context, req *pb.SnapshotRequest, disks []storage.DomainDisk) (*pb.SnapshotResponse, error) {
	imgs, err := openImages(disks)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	st, err := libvirt.GetVMStatus(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read VM state: %v", err)
	}
	name := req.Name
	if req.Operation != pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE {
		name = req.SnapshotId
	}
	snap := models.RBDSnapshotPrefix + name
	if req.Operation != pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST && !models.ValidRBDSnapshotName(name) {
		return nil, status.Error(codes.InvalidArgument, "a snapshot of a VM on Ceph RBD needs a name of letters, digits, dots, dashes and underscores")
	}

	switch req.Operation {
	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE:
		if st != libvirt.VMStatusStopped && !req.GetDiskOnly() {
			return nil, status.Error(codes.FailedPrecondition, "this VM's disks are on Ceph RBD, where a snapshot holds the disks and not the memory; take a disk-only snapshot, or stop the VM first")
		}
		// Listed before the guest is frozen. An image that has the snapshot
		// already was given it by an earlier try whose answer was lost (the
		// panel lets a VM have each name once).
		var missing []ceph.Image
		for _, img := range imgs {
			have, err := img.Snapshots(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
			}
			if !slices.ContainsFunc(have, func(h ceph.Snapshot) bool { return h.Name == snap }) {
				missing = append(missing, img)
			}
		}
		thaw := func() {}
		if req.GetQuiesce() && st == libvirt.VMStatusRunning && len(missing) > 0 {
			if thaw, err = libvirt.FreezeGuest(req.VmId); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to create snapshot: the guest agent could not freeze the filesystems (%v); take the snapshot without quiescing", err)
			}
		}
		var made []ceph.Image
		for _, img := range missing {
			if err = img.SnapCreate(ctx, snap); err != nil {
				break
			}
			made = append(made, img)
		}
		thaw()
		if err != nil {
			for _, img := range made {
				_ = img.SnapRemove(context.WithoutCancel(ctx), snap)
			}
			return nil, status.Errorf(codes.Internal, "failed to create snapshot: %v", err)
		}
		return &pb.SnapshotResponse{Success: true, Operation: req.Operation, Snapshot: &pb.SnapshotInfo{
			SnapshotId: name, Name: name, Description: req.Description, CreatedAt: timestamppb.Now(), VmState: mapVMStatusToPBState(st)}}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST:
		have, err := imgs[0].Snapshots(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
		}
		resp := &pb.SnapshotResponse{Success: true, Operation: req.Operation}
		for _, h := range have {
			if n, ok := strings.CutPrefix(h.Name, models.RBDSnapshotPrefix); ok {
				resp.Snapshots = append(resp.Snapshots, &pb.SnapshotInfo{SnapshotId: n, Name: n,
					CreatedAt: timestamppb.New(h.CreatedAt), VmState: pb.VMState_VM_STATE_STOPPED})
			}
		}
		return resp, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE:
		if st != libvirt.VMStatusStopped {
			return nil, status.Error(codes.FailedPrecondition, "stop the VM first: a snapshot of disks on Ceph RBD is rolled back only while the VM is off")
		}
		// A rollback cut short leaves a disk half old, half new: it runs to its
		// end whatever happens to the caller, and the VM is held off meanwhile.
		if !migrations.Start(req.VmId, snapshotRun, "Rolling back to a snapshot", nil) {
			return nil, status.Error(codes.FailedPrecondition, errDisksBusy.Error())
		}
		defer migrations.Finish(req.VmId, snapshotRun, nil)
		run := context.WithoutCancel(ctx)
		var with []ceph.Image
		for _, img := range imgs {
			have, err := img.Snapshots(run)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to restore snapshot: %v", err)
			}
			for _, h := range have {
				if h.Name == snap {
					with = append(with, img)
				}
			}
			// A VM running on the disk from another node must not have it
			// rolled back under it. (A VM just powered off here is listed a
			// little longer, and is waited for.)
			if err := img.CheckUnused(run, ceph.WatcherGrace); err != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "failed to restore snapshot: %v", err)
			}
		}
		if len(with) == 0 {
			return nil, status.Errorf(codes.NotFound, "snapshot not found: %s", name)
		}
		for _, img := range with {
			if err := img.SnapRollback(run, snap); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to revert %s to the snapshot; run the restore again before starting the VM: %v", img.Spec(), err)
			}
		}
		return &pb.SnapshotResponse{Success: true, Operation: req.Operation,
			Snapshot: &pb.SnapshotInfo{SnapshotId: name, VmState: pb.VMState_VM_STATE_STOPPED}}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE:
		for _, img := range imgs {
			if err := img.SnapRemove(ctx, snap); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to delete snapshot: %v", err)
			}
		}
		return &pb.SnapshotResponse{Success: true, Operation: req.Operation}, nil
	}
	return nil, status.Errorf(codes.InvalidArgument, "unknown snapshot operation: %v", req.Operation)
}

// holdRBDForBackup takes the snapshot a backup of disks on RBD reads from:
// one per disk, all under a running guest's frozen file systems when its
// agent answers. release removes them. The disks are copied in full each
// time: a raw image keeps no record of what changed.
func holdRBDForBackup(ctx context.Context, vmID string, disks []storage.DomainDisk) (release func(), consistency string, err error) {
	imgs, err := openImages(disks)
	if err != nil {
		return nil, "", err
	}
	release = func() {
		for _, img := range imgs {
			if rerr := img.SnapRemove(context.WithoutCancel(ctx), rbdBackupSnap); rerr != nil {
				log.Printf("[NodeAgent] WARNING: VM %s: backup snapshot of %s not removed: %v", vmID, img.Spec(), rerr)
			}
		}
	}
	// One left by a backup that died with the agent.
	for _, img := range imgs {
		if err := img.SnapRemove(ctx, rbdBackupSnap); err != nil {
			return nil, "", err
		}
	}
	consistency = libvirt.ConsistencyStopped
	thaw := func() {}
	if running, _ := libvirt.IsRunning(vmID); running {
		consistency = libvirt.ConsistencyFrozen
		if thaw, err = libvirt.FreezeGuest(vmID); err != nil {
			log.Printf("[NodeAgent] VM %s: guest file systems not frozen (%v); backing up without", vmID, err)
			consistency, thaw = libvirt.ConsistencyCrash, func() {}
		}
	}
	for _, img := range imgs {
		if err = img.SnapCreate(ctx, rbdBackupSnap); err != nil {
			break
		}
	}
	thaw()
	if err != nil {
		release()
		return nil, "", err
	}
	return release, consistency, nil
}

// rbdExportSource is what qemu-img reads to copy a disk on RBD for a backup.
func rbdExportSource(path string) (string, error) {
	img, err := ceph.OpenImage(path)
	if err != nil {
		return "", err
	}
	return img.QemuURL(rbdBackupSnap), nil
}

// rbdDiskSize is the size of the disk at an RBD path; 0 when it cannot be read.
func rbdDiskSize(ctx context.Context, path string) int64 {
	img, err := ceph.OpenImage(path)
	if err != nil {
		return 0
	}
	n, _ := img.Size(ctx)
	return n
}

// restoreOntoRBD replaces a stopped VM's disk on RBD with a downloaded backup
// image. The disk keeps at least its size; its RBD snapshots go with the old
// contents, as a qcow2 disk's do.
func restoreOntoRBD(ctx context.Context, path, image string) error {
	img, err := ceph.OpenImage(path)
	if err != nil {
		return err
	}
	have, err := img.Size(ctx)
	if err != nil {
		return err
	}
	return img.ReplaceWith(ctx, image, "qcow2", have)
}

// rbdMigrationProblems says what stops a VM whose disks stay on RBD from
// landing on this node through the pool at poolRef.
func rbdMigrationProblems(poolType, poolPath string, creates int) []string {
	if _, _, err := requestRBDPool(poolType, poolPath); err != nil {
		return []string{err.Error()}
	}
	if creates > 0 {
		return []string{"a move does not copy disks into a Ceph RBD pool; only a VM whose disks are in the pool already can land on it"}
	}
	return nil
}

// errRBDMove refuses a disk move that starts or ends on RBD.
var errRBDMove = errors.New("a disk is not moved between a Ceph RBD pool and another pool yet; back the VM up and restore it as a new VM on the other storage")
