package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func diskOpErr(code pb.ErrorCode, err error) *pb.ErrorResponse {
	return &pb.ErrorResponse{Code: code, Message: err.Error()}
}

// ResizeVMDisk grows one disk of a VM, running or not.
func (s *NodeAgentService) ResizeVMDisk(ctx context.Context, req *pb.ResizeVMDiskRequest) (*pb.ResizeVMDiskResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	if !migration.ValidUUID(req.VmId) || !migration.ValidTarget(req.Target) || req.SizeGb <= 0 || req.SizeGb > 1<<20 {
		return &pb.ResizeVMDiskResponse{Error: diskOpErr(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("a VM id, a disk target and a size in GB are required"))}, nil
	}
	s.crashes.Hold(req.VmId)
	live, size, err := libvirt.ResizeDiskTarget(req.VmId, req.Target, int(req.SizeGb), growLV(ctx, lvmPoolsOf(req.GetLvmPools())))
	if err != nil {
		return &pb.ResizeVMDiskResponse{Error: diskOpErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	log.Printf("[DiskOps] VM %s disk %s is %d bytes (live=%v)", req.VmId, req.Target, size, live)
	return &pb.ResizeVMDiskResponse{Success: true, Live: live, SizeBytes: size}, nil
}

// diskMove is one move MoveVMDisk runs.
type diskMove struct {
	vmID, moveID string
	disk         storage.DomainDisk
	size         int64
	live         bool
	dest         string
	destFormat   string
	lvPool       storage.LVMPool
	onLVM        bool
	pools        []storage.LVMPool
}

// MoveVMDisk starts moving one disk of a VM to another pool of this node.
func (s *NodeAgentService) MoveVMDisk(ctx context.Context, req *pb.MoveVMDiskRequest) (*pb.MoveVMDiskResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(code pb.ErrorCode, err error) (*pb.MoveVMDiskResponse, error) {
		return &pb.MoveVMDiskResponse{Error: diskOpErr(code, err)}, nil
	}
	if !migration.ValidUUID(req.VmId) || req.MoveId == "" || len(req.MoveId) > 64 || !migration.ValidTarget(req.Target) {
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, errors.New("a VM id, a move id and a disk target are required"))
	}
	if migrations.Running(req.VmId, req.MoveId) {
		return &pb.MoveVMDiskResponse{Success: true}, nil
	}
	m, err := planDiskMove(ctx, req)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	if !migrations.Start(req.VmId, req.MoveId, "Copying disk "+req.Target, cancel) {
		cancel()
		return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, errors.New("another move or migration of this VM is running"))
	}
	go func() {
		defer cancel()
		log.Printf("[DiskMove] %s disk %s: %s -> %s (live=%v)", m.vmID, m.disk.Target, m.disk.Path, m.dest, m.live)
		err := m.run(runCtx)
		migrations.Finish(m.vmID, m.moveID, err)
		log.Printf("[DiskMove] %s disk %s ended: %v", m.vmID, m.disk.Target, err)
	}()
	return &pb.MoveVMDiskResponse{Success: true, Live: m.live, DestPath: m.dest}, nil
}

// planDiskMove checks a move can be made and works out where the disk goes.
func planDiskMove(ctx context.Context, req *pb.MoveVMDiskRequest) (*diskMove, error) {
	disk, running, err := libvirt.FindDisk(req.VmId, req.Target)
	if err != nil {
		return nil, err
	}
	if storage.IsBackupOverlay(disk.Path) || libvirt.RefuseDuringBackup(req.VmId) != nil {
		return nil, errors.New("a backup is copying the VM's disks; try again when it has finished")
	}
	if n, err := libvirt.SnapshotCount(req.VmId); err != nil || n > 0 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("the VM has %d snapshots, which cannot move with the disk; delete them first", n)
	}
	m := &diskMove{vmID: req.VmId, moveID: req.MoveId, disk: disk, live: running, pools: lvmPoolsOf(req.GetLvmPools())}
	if disk.Block {
		m.size, err = storage.DeviceSize(disk.Path)
	} else {
		var info *storage.ImageInfo
		if info, err = storage.NewQCOW2Manager().ImageInfo(disk.Path); err == nil {
			m.size = info.VirtualSize
		}
	}
	if err != nil || m.size <= 0 {
		return nil, fmt.Errorf("the size of %s could not be read: %v", disk.Path, err)
	}
	if m.lvPool, m.onLVM, err = requestLVMPool(req.DestPoolType, req.DestPoolPath); err != nil {
		return nil, err
	}
	if m.onLVM {
		if disk.Block {
			if lv, lerr := lvm.Lookup(ctx, disk.Path); lerr == nil && lv.VG == m.lvPool.VG && lv.Pool == m.lvPool.Thin {
				return nil, fmt.Errorf("the disk is already in %s", m.lvPool)
			}
		}
		if problems := lvmTargetProblems(ctx, m.lvPool, m.size, 0); len(problems) > 0 {
			return nil, errors.New(strings.Join(problems, "; "))
		}
		name, err := lvm.FreeName(ctx, m.lvPool, req.VmId, req.Target)
		if err != nil {
			return nil, err
		}
		m.dest, m.destFormat = "/dev/"+m.lvPool.VG+"/"+name, "raw"
		return m, nil
	}
	dir, err := storage.ResolvePoolDir(req.DestPoolPath, defaultImageDir)
	if err != nil {
		return nil, err
	}
	if !disk.Block && filepath.Dir(disk.Path) == dir {
		return nil, fmt.Errorf("the disk is already in %s", dir)
	}
	base := strings.TrimSuffix(filepath.Base(disk.Path), filepath.Ext(disk.Path))
	if disk.Block {
		// A volume becomes a file named as a create or attach names one, so
		// the orphan finder knows it as this VM's.
		base = req.VmId
		if req.Target != "vda" {
			base += "-data-" + req.Target
		}
	}
	m.dest, m.destFormat = filepath.Join(dir, base+".qcow2"), "qcow2"
	if _, err := os.Lstat(m.dest); err == nil {
		return nil, fmt.Errorf("%s already exists", m.dest)
	}
	// A qcow2 copy takes what the guest wrote, at most the whole disk.
	need := m.size
	if fi, err := os.Stat(disk.Path); err == nil && !disk.Block && fi.Size() < need {
		need = fi.Size()
	}
	if free, err := freeBytes(dir); err == nil && free < need+volumeReserveBytes {
		return nil, fmt.Errorf("%s has %d GB free; the disk needs up to %d GB there, and %d GB kept free", dir, free>>30, need>>30, volumeReserveBytes>>30)
	}
	return m, nil
}

func (m *diskMove) run(ctx context.Context) error {
	if err := m.copy(ctx); err != nil {
		m.discardCopy()
		return err
	}
	migrations.Update(m.vmID, m.moveID, "Removing the old disk", m.size, m.size)
	m.removeOld()
	return nil
}

// copy makes the disk's copy at dest and points the VM at it.
func (m *diskMove) copy(ctx context.Context) error {
	if m.live {
		if m.onLVM {
			if _, err := createDiskLV(ctx, m.lvPool, filepath.Base(m.dest), m.size); err != nil {
				return err
			}
		} else if err := migration.CreateImage(ctx, m.dest, "qcow2", m.size); err != nil {
			return err
		}
		return libvirt.BlockCopyDisk(ctx, m.vmID, m.disk.Target, m.dest, m.destFormat, func(total, done int64) {
			migrations.Update(m.vmID, m.moveID, "", total, done)
		})
	}
	migrations.Update(m.vmID, m.moveID, "Copying disk "+m.disk.Target+" while the VM is off", m.size, 0)
	if m.onLVM {
		if _, err := lvm.Provision(ctx, m.lvPool, filepath.Base(m.dest), m.size, m.disk.Path, m.disk.Format); err != nil {
			return err
		}
	} else {
		part := fmt.Sprintf("%s.move-%s.part", m.dest, shortID(m.moveID))
		defer os.Remove(part)
		out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-f", m.disk.Format, "-O", "qcow2", m.disk.Path, part).CombinedOutput()
		if err != nil {
			return fmt.Errorf("copying %s failed: %v (%s)", m.disk.Path, err, strings.TrimSpace(string(out)))
		}
		if err := os.Rename(part, m.dest); err != nil {
			return err
		}
	}
	return libvirt.RepointStoppedDisk(m.vmID, m.disk.Target, m.dest, m.destFormat)
}

// discardCopy removes a copy the VM was never pointed at.
func (m *diskMove) discardCopy() {
	if d, _, err := libvirt.FindDisk(m.vmID, m.disk.Target); err == nil && d.Path == m.dest {
		return // the VM uses it: the move went through after all
	}
	if m.onLVM {
		discardNewDisk(m.vmID, m.dest)
	} else {
		_ = os.Remove(m.dest)
	}
}

// removeOld removes the disk the VM left, when nothing else uses it and, for
// a volume, MaburVM created it for this VM. A volume of an imported VM stays
// and shows in the list of orphaned disks.
func (m *diskMove) removeOld() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if m.disk.Block {
		lv, err := lvm.Lookup(ctx, m.disk.Path)
		if err != nil || !storage.Owned(lv, m.vmID) {
			log.Printf("[DiskMove] kept %s: not a volume MaburVM created for VM %s; delete it from the orphaned disks list", m.disk.Path, m.vmID)
			return
		}
		if err := removeDetachedLV(ctx, m.vmID, m.disk.Path, m.pools); err != nil {
			log.Printf("[DiskMove] WARNING: kept %s: %v", m.disk.Path, err)
		}
		return
	}
	// Backing chains count: another disk may be built on this one.
	inUse, err := usedPaths(ctx)
	if err != nil || inUse[filepath.Clean(m.disk.Path)] {
		log.Printf("[DiskMove] kept %s: it may still be in use (%v)", m.disk.Path, err)
		return
	}
	if err := os.Remove(m.disk.Path); err != nil {
		log.Printf("[DiskMove] WARNING: could not remove %s: %v", m.disk.Path, err)
	}
}

func shortID(id string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(id))
	if len(clean) > 12 {
		clean = clean[:12]
	}
	return clean
}

// usedPaths is every file and device a domain on this node uses, with the
// images their disks are built on.
func usedPaths(ctx context.Context) (map[string]bool, error) {
	inUse, err := libvirt.DiskSourceFiles()
	if err != nil {
		return nil, err
	}
	for p := range inUse {
		if models.IsBlockDevicePath(p) {
			if real, err := filepath.EvalSymlinks(p); err == nil {
				inUse[real] = true
			}
			continue
		}
		if !strings.HasSuffix(p, ".qcow2") {
			continue
		}
		chain, err := storage.BackingChain(ctx, p)
		if err != nil {
			if _, serr := os.Stat(p); errors.Is(serr, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, f := range chain {
			inUse[f] = true
		}
	}
	return inUse, nil
}

// busyVMs are the VMs this node is creating, moving or receiving now: their
// files exist before any domain uses them.
func busyVMs() map[string]bool {
	out := map[string]bool{}
	createsInFlight.Lock()
	for id := range createsInFlight.ids {
		out[id] = true
	}
	createsInFlight.Unlock()
	for _, e := range storage.ListExports() {
		out[e.VMID] = true
	}
	if entries, err := os.ReadDir(migrationLayout().PreparedDir()); err == nil {
		for _, e := range entries {
			out[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	return out
}

// listOrphans finds the disk files and volumes in the pools no domain uses.
func listOrphans(ctx context.Context, dirPools []string, lvmPools []*pb.LVMPool) ([]storage.Orphan, error) {
	inUse, err := usedPaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not tell which disks VMs use: %w", err)
	}
	busy := busyVMs()
	var dirs []string
	for _, d := range dirPools {
		if d = filepath.Clean(d); filepath.IsAbs(d) && d != templateCacheDir && d != customerISODir {
			dirs = append(dirs, d)
		}
	}
	now := time.Now()
	var out []storage.Orphan
	for _, o := range storage.FindOrphanFiles(dirs, inUse, storage.OrphanMinAge, now) {
		if !busy[o.VMID] && !migrations.Busy(o.VMID) {
			out = append(out, o)
		}
	}
	// The template cache holds templates; only clone copies left there count.
	for _, o := range storage.FindOrphanFiles([]string{templateCacheDir}, inUse, storage.OrphanMinAge, now) {
		if strings.HasPrefix(filepath.Base(o.Path), "clone-") && !migrations.Busy(o.VMID) {
			out = append(out, o)
		}
	}
	pools := lvmPoolsOf(lvmPools)
	seen := map[string]bool{}
	for _, p := range pools {
		if seen[p.VG] {
			continue
		}
		seen[p.VG] = true
		lvs, err := lvm.List(ctx, p.VG)
		if err != nil {
			return nil, fmt.Errorf("volume group %s: %w", p.VG, err)
		}
		for _, o := range storage.OrphanLVs(lvs, pools, inUse) {
			if !busy[o.VMID] && !migrations.Busy(o.VMID) {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// ListOrphanDisks lists the disks in the pools no domain uses.
func (s *NodeAgentService) ListOrphanDisks(ctx context.Context, req *pb.ListOrphanDisksRequest) (*pb.ListOrphanDisksResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	orphans, err := listOrphans(ctx, req.DirPools, req.LvmPools)
	if err != nil {
		return &pb.ListOrphanDisksResponse{Error: diskOpErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)}, nil
	}
	resp := &pb.ListOrphanDisksResponse{Success: true}
	for _, o := range orphans {
		resp.Disks = append(resp.Disks, &pb.OrphanDisk{Path: o.Path, Kind: o.Kind, SizeBytes: o.Size, ModifiedUnix: o.Modified,
			Reason: o.Reason, VmId: o.VMID, Maburvm: o.MaburVM, Pool: o.Pool})
	}
	return resp, nil
}

// DeleteOrphanDisk removes one orphaned disk after finding it orphaned again.
func (s *NodeAgentService) DeleteOrphanDisk(ctx context.Context, req *pb.DeleteOrphanDiskRequest) (*pb.DeleteOrphanDiskResponse, error) {
	if err := requireAuth(ctx); err != nil {
		return nil, err
	}
	fail := func(err error) (*pb.DeleteOrphanDiskResponse, error) {
		return &pb.DeleteOrphanDiskResponse{Error: diskOpErr(pb.ErrorCode_ERROR_CODE_CONFLICT, err)}, nil
	}
	if !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path {
		return fail(fmt.Errorf("%q is not a path", req.Path))
	}
	orphans, err := listOrphans(ctx, req.DirPools, req.LvmPools)
	if err != nil {
		return fail(err)
	}
	var o *storage.Orphan
	for i := range orphans {
		if orphans[i].Path == req.Path {
			o = &orphans[i]
		}
	}
	switch {
	case o == nil:
		return fail(fmt.Errorf("%s is no longer an unused disk in a registered pool; nothing was removed", req.Path))
	case o.Size != req.SizeBytes || o.Modified != req.ModifiedUnix:
		return fail(fmt.Errorf("%s changed since it was listed; list the orphaned disks again", req.Path))
	case !o.MaburVM && !req.AllowForeign:
		return fail(fmt.Errorf("%s was not made by MaburVM; confirm deleting a disk MaburVM did not make", req.Path))
	}
	if o.Kind == "lv" {
		lv, err := lvm.Lookup(ctx, o.Path)
		if err != nil {
			return fail(err)
		}
		if err := storage.CheckRemovable(lv, o.VMID, lvmPoolsOf(req.LvmPools), true); err != nil {
			return fail(err)
		}
		if used, err := usedByDomain(o.Path); err != nil || used {
			return fail(fmt.Errorf("%s may be a VM's disk; nothing was removed", o.Path))
		}
		if err := lvm.Remove(ctx, lv); err != nil {
			return fail(err)
		}
	} else if err := os.Remove(o.Path); err != nil {
		return fail(err)
	}
	log.Printf("[DiskOps] removed orphaned disk %s (%s, %d bytes)", o.Path, o.Reason, o.Size)
	return &pb.DeleteOrphanDiskResponse{Success: true}, nil
}

// maxUploadImage bounds an image the panel uploads (an ISO).
const maxUploadImage = 64 << 30

// PutImage stores an image uploaded to the panel in the template cache.
func (s *NodeAgentService) PutImage(stream pb.NodeAgent_PutImageServer) error {
	if err := requireAuth(stream.Context()); err != nil {
		return err
	}
	fail := func(err error) error {
		log.Printf("[PutImage] refused: %v", err)
		return stream.SendAndClose(&pb.PutImageResponse{Error: diskOpErr(pb.ErrorCode_ERROR_CODE_INTERNAL, err)})
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sum := strings.ToLower(first.Sha256)
	if !models.ValidUploadName(first.Name) || !models.ValidSHA256(sum) || first.SizeBytes <= 0 || first.SizeBytes > maxUploadImage {
		return fail(errors.New("an image needs a plain file name, a sha256 and a size"))
	}
	final := filepath.Join(templateCacheDir, storage.UploadCacheName(sum, first.Name))
	if _, err := os.Stat(final); err == nil {
		// Named by its checksum, so the copy here is this image.
		return stream.SendAndClose(&pb.PutImageResponse{Success: true, LocalPath: final})
	}
	if err := os.MkdirAll(templateCacheDir, 0o755); err != nil {
		return fail(err)
	}
	if free, err := freeBytes(templateCacheDir); err == nil && free < first.SizeBytes+volumeReserveBytes {
		return fail(fmt.Errorf("%s has %d GB free; the image needs %d GB, and %d GB kept free", templateCacheDir, free>>30, first.SizeBytes>>30, volumeReserveBytes>>30))
	}
	part := fmt.Sprintf("%s.%d.part", final, time.Now().UnixNano())
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fail(err)
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(part)
		}
	}()
	h := sha256.New()
	w := io.MultiWriter(f, h)
	var n int64
	for chunk := first; ; {
		if len(chunk.Data) > 0 {
			if n += int64(len(chunk.Data)); n > first.SizeBytes {
				return fail(errors.New("the image is larger than its header said"))
			}
			if _, err := w.Write(chunk.Data); err != nil {
				return fail(err)
			}
		}
		if chunk, err = stream.Recv(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
	}
	if n != first.SizeBytes {
		return fail(fmt.Errorf("received %d of %d bytes", n, first.SizeBytes))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fail(fmt.Errorf("checksum mismatch: the image should have sha256 %s and has %s", sum, got))
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := os.Rename(part, final); err != nil {
		return fail(err)
	}
	keep = true
	log.Printf("[PutImage] stored %s (%d bytes)", final, n)
	return stream.SendAndClose(&pb.PutImageResponse{Success: true, LocalPath: final})
}
