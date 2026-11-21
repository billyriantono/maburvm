package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/storage"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
)

// lvm runs the node's LVM tools for VM disks on LVM pools.
var lvm = storage.NewLVM()

// lvmPoolsOf reads the registered LVM pools a request carries.
func lvmPoolsOf(in []*pb.LVMPool) []storage.LVMPool {
	out := make([]storage.LVMPool, 0, len(in))
	for _, p := range in {
		if models.ValidLVMName(p.GetVg()) && (p.GetThinPool() == "" || models.ValidLVMName(p.GetThinPool())) {
			out = append(out, storage.LVMPool{VG: p.GetVg(), Thin: p.GetThinPool()})
		}
	}
	return out
}

// requestLVMPool is the LVM pool a request places a disk in; false for a
// directory pool.
func requestLVMPool(poolType, poolPath string) (storage.LVMPool, bool, error) {
	if !models.IsLVMPoolType(poolType) {
		return storage.LVMPool{}, false, nil
	}
	p, err := storage.ParseLVMPool(poolType, poolPath)
	return p, err == nil, err
}

// blockScratchDir holds what a disk on a block device needs as a file for a
// while: a backup's export, a download being restored. TEMPLATE_CACHE_DIR
// moves it off the root filesystem.
func blockScratchDir() string { return templateCacheDir }

// growLV grows a VM's logical volume when it lives in a registered pool.
func growLV(ctx context.Context, pools []storage.LVMPool) func(dev string, want int64) error {
	return func(dev string, want int64) error {
		lv, err := lvm.Lookup(ctx, dev)
		if err != nil {
			return err
		}
		if _, ok := storage.HeldBy(lv, pools); !ok {
			return fmt.Errorf("the disk %s is not in a storage pool registered with the panel; register its volume group as an LVM pool to resize it", dev)
		}
		if err := lvm.Extend(ctx, lv, want); err != nil || lv.Pool != "" {
			return err
		}
		// A thick volume's new extents hold what an earlier volume left there:
		// cleared before the VM is told of them, even if the caller gives up.
		end, err := storage.DeviceSize(dev)
		if err != nil {
			return err
		}
		if err := lvm.ZeroRange(context.WithoutCancel(ctx), dev, lv.Size, end); err != nil {
			return fmt.Errorf("the volume grew but its new space could not be cleared: %w", err)
		}
		return nil
	}
}

// usedByDomain reports whether any domain on this node uses the device, by
// any of its names (/dev/vg/lv and /dev/mapper/vg-lv are the same device).
func usedByDomain(dev string) (bool, error) {
	inUse, err := libvirt.DiskSourceFiles()
	if err != nil {
		return true, err
	}
	want, err := filepath.EvalSymlinks(dev)
	if err != nil {
		want = dev
	}
	for p := range inUse {
		if !models.IsBlockDevicePath(p) {
			continue
		}
		if real, rerr := filepath.EvalSymlinks(p); p == dev || (rerr == nil && real == want) {
			return true, nil
		}
	}
	return false, nil
}

// removeLV removes the logical volume at dev once it is proven removable (see
// storage.CheckRemovable) and no domain uses it.
func removeLV(ctx context.Context, vmID, dev string, pools []storage.LVMPool, allowForeign bool) error {
	lv, err := lvm.Lookup(ctx, dev)
	if err != nil {
		return err
	}
	if err := storage.CheckRemovable(lv, vmID, pools, allowForeign); err != nil {
		return err
	}
	if used, err := usedByDomain(dev); err != nil || used {
		if err != nil {
			return fmt.Errorf("could not tell whether a VM still uses %s: %w", dev, err)
		}
		return fmt.Errorf("%s is still a disk of a VM on this node", dev)
	}
	if err := lvm.Remove(ctx, lv); err != nil {
		return err
	}
	log.Printf("[NodeAgent] removed logical volume %s", lv.Path())
	return nil
}

// discardNewDisk removes a disk a create or attach made before it failed: a
// file, or a logical volume MaburVM created for the VM.
func discardNewDisk(vmID, path string) {
	if !models.IsBlockDevicePath(path) {
		_ = os.Remove(path)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lv, err := lvm.Lookup(ctx, path)
	if err != nil || !storage.Owned(lv, vmID) || lv.Open() {
		log.Printf("[NodeAgent] WARNING: left %s in place after a failed create: not provably this VM's new volume (%v)", path, err)
		return
	}
	if err := lvm.Remove(ctx, lv); err != nil {
		log.Printf("[NodeAgent] WARNING: could not remove %s after a failed create: %v", path, err)
	}
}

// clearOrphanLV removes a volume an earlier create of this VM left behind.
// Called only when no domain exists for the VM and no create of it runs.
func clearOrphanLV(ctx context.Context, pool storage.LVMPool, vmID, dev string) error {
	lv, err := lvm.Lookup(ctx, dev)
	if err != nil {
		return nil // not there
	}
	if !storage.Owned(lv, vmID) {
		return fmt.Errorf("%s exists and was not created by MaburVM for this VM; it is left alone", dev)
	}
	log.Printf("[NodeAgent] CREATE: removing orphaned volume %s from a previous failed attempt", dev)
	return removeLV(ctx, vmID, dev, []storage.LVMPool{pool}, false)
}

// lvDataDiskName names a new data disk volume of the VM.
func lvDataDiskName(vmID string) string {
	return models.LVMDiskName(vmID, "d"+strconv.FormatInt(time.Now().UnixNano(), 36))
}

// fetchRestoreDisksLVM makes each data disk of a backup restored as a new VM
// into a volume of the pool. On failure nothing is left.
func fetchRestoreDisksLVM(client sharedstorage.Store, pool storage.LVMPool, vmID string, files []*pb.BackupDiskFile) ([]*pb.BackupDiskFile, error) {
	var out []*pb.BackupDiskFile
	undo := func() {
		for _, d := range out {
			discardNewDisk(vmID, d.GetPath())
		}
	}
	for _, f := range files {
		if !validDataTarget(f.GetTarget()) {
			undo()
			return nil, fmt.Errorf("the backup names disk %q, which is not a data disk", f.GetTarget())
		}
		// Left by an earlier try of this create that died: a retry would stop on it for good.
		if err := clearOrphanLV(context.Background(), pool, vmID, "/dev/"+pool.VG+"/"+models.LVMDiskName(vmID, f.GetTarget())); err != nil {
			undo()
			return nil, err
		}
		dev, err := restoreOntoNewLV(client, pool, vmID, f.GetTarget(), f.GetLayers(), f.GetKey(), f.GetChecksum(), 0)
		if err != nil {
			undo()
			return nil, fmt.Errorf("disk %s: %w", f.GetTarget(), err)
		}
		d := &pb.BackupDiskFile{Target: f.GetTarget(), Path: dev}
		if n, serr := storage.DeviceSize(dev); serr == nil {
			d.VirtualSizeBytes = n
		}
		out = append(out, d)
	}
	return out, nil
}

// restoreOntoNewLV downloads a stored disk image, checks it, and makes it a
// new volume of the VM named for target.
func restoreOntoNewLV(client sharedstorage.Store, pool storage.LVMPool, vmID, target string, layers []*pb.BackupLayer, key, sum string, size int64) (string, error) {
	if err := os.MkdirAll(blockScratchDir(), 0o755); err != nil {
		return "", err
	}
	part := filepath.Join(blockScratchDir(), vmID+"-"+target+"-restore.qcow2.part")
	defer os.Remove(part)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
	defer cancel()
	if _, err := fetchBackupImage(ctx, client, layers, key, part, sum); err != nil {
		return "", err
	}
	return lvm.Provision(ctx, pool, models.LVMDiskName(vmID, target), size, part, "qcow2")
}

// removeVMVolumes decides, for each block device a just-destroyed VM had,
// whether it goes with it. A volume MaburVM created for the VM in a
// registered pool goes; another (an imported VM's) goes only when the
// operator said so and it is in a registered pool. Everything else is kept and
// reported, so the panel can list it.
func removeVMVolumes(ctx context.Context, vmID string, disks []storage.DomainDisk, pools []storage.LVMPool, foreign bool) []*pb.KeptVolume {
	var kept []*pb.KeptVolume
	for _, d := range disks {
		if !d.Block {
			continue
		}
		k := &pb.KeptVolume{Path: d.Path, Target: d.Target}
		if n, err := storage.DeviceSize(d.Path); err == nil {
			k.SizeBytes = n
		}
		lv, err := lvm.Lookup(ctx, d.Path)
		if err != nil {
			k.Reason = "not a logical volume; MaburVM never removes other devices"
			kept = append(kept, k)
			continue
		}
		k.SizeBytes = lv.Size
		if p, ok := storage.HeldBy(lv, pools); ok {
			k.Vg, k.ThinPool = p.VG, p.Thin
		}
		owned := storage.Owned(lv, vmID)
		if !owned && !foreign {
			k.Reason = "not created by MaburVM; kept until an operator deletes it"
			kept = append(kept, k)
			continue
		}
		if err := removeLV(ctx, vmID, d.Path, pools, !owned); err != nil {
			k.Reason = err.Error()
			kept = append(kept, k)
		}
	}
	return kept
}

// lvmCapacities measures and checks the LVM pools a storage report asks for.
func lvmCapacities(ctx context.Context, pools []*pb.LVMPool) []*pb.LVMPoolCapacity {
	var out []*pb.LVMPoolCapacity
	for _, p := range pools {
		c := &pb.LVMPoolCapacity{Vg: p.GetVg(), ThinPool: p.GetThinPool()}
		out = append(out, c)
		pool := storage.LVMPool{VG: p.GetVg(), Thin: p.GetThinPool()}
		if !models.ValidLVMName(pool.VG) || (pool.Thin != "" && !models.ValidLVMName(pool.Thin)) {
			c.Error = "not a valid volume group or thin pool name"
			continue
		}
		capacity, err := lvm.Capacity(ctx, pool)
		if err != nil {
			c.Error = err.Error()
			continue
		}
		c.Exists = true
		c.TotalBytes, c.UsedBytes, c.AvailableBytes, c.MetadataPercent = capacity.Total, capacity.Used, capacity.Available, capacity.MetadataPercent
		if err := lvm.Check(ctx, pool); err != nil {
			c.Error = err.Error()
		}
	}
	return out
}

// cloneSourceFormat is the format a local clone source is read as: the source
// VM's own disk format, so a raw disk is never probed. "" for a template.
func cloneSourceFormat(ref string) string {
	spec, ok := strings.CutPrefix(ref, "vm://")
	if !ok || strings.Contains(spec, "/") {
		return ""
	}
	disks, err := libvirt.BackupDisks(spec)
	if err != nil || len(disks) == 0 {
		return ""
	}
	return disks[0].Format
}

// provisionLVMDisk makes a new VM's primary disk a volume of the pool: from
// the backup it is restored from (with its data disks), or from its template
// or clone source. The volume is the size asked for, or the image's if larger.
func provisionLVMDisk(req *pb.VMCommandRequest, imageRef string, pool storage.LVMPool, restored *[]*pb.BackupDiskFile) (string, error) {
	cfg := req.GetConfig()
	size := cfg.GetResources().GetDiskGb() << 30
	if key, once := strings.CutPrefix(imageRef, "s3://"); once && cfg.GetMetadata()[models.ImageOnceKey] != "" {
		client, _, err := backupStore(context.Background(), req.GetObjectStore())
		if err != nil {
			return "", fmt.Errorf("the backup destination cannot be used: %w", err)
		}
		defer client.Close()
		dev, err := restoreOntoNewLV(client, pool, req.VmId, "vda", cfg.GetRestoreLayers(), key, cfg.GetMetadata()[models.ImageSHA256Key], size)
		if err != nil {
			return "", err
		}
		extra, err := fetchRestoreDisksLVM(client, pool, req.VmId, cfg.GetRestoreDisks())
		if err != nil {
			discardNewDisk(req.VmId, dev)
			return "", err
		}
		*restored = extra
		return dev, nil
	}
	templatePath, err := resolveTemplateImage(imageRef, req.GetObjectStore(), cfg.GetMetadata()[models.ImageSHA256Key])
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
	defer cancel()
	dev, err := lvm.Provision(ctx, pool, models.LVMDiskName(req.VmId, "vda"), size, templatePath, cloneSourceFormat(imageRef))
	if err != nil {
		return "", fmt.Errorf("failed to provision disk from template: %w", err)
	}
	return dev, nil
}

// removeDetachedLV removes a volume just detached from a VM. A running guest
// lets go of a hot-unplugged disk in its own time, so an open volume is
// waited for briefly.
func removeDetachedLV(ctx context.Context, vmID, dev string, pools []storage.LVMPool) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = removeLV(ctx, vmID, dev, pools, false); err == nil || !(strings.Contains(err.Error(), "in use") || strings.Contains(err.Error(), "still a disk")) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

// createDiskLV makes a tagged volume of at least size bytes for a disk about
// to be copied in. A thick volume is rounded up to whole extents, and what is
// past size may still hold an earlier volume's data the copy never reaches:
// that part is zeroed now. (A thin volume reads as zeroes already.)
func createDiskLV(ctx context.Context, pool storage.LVMPool, name string, size int64) (string, error) {
	dev, err := lvm.Create(ctx, pool, name, size)
	if err != nil || pool.Thin != "" {
		return dev, err
	}
	end, err := storage.DeviceSize(dev)
	if err == nil {
		err = lvm.ZeroRange(ctx, dev, size, end)
	}
	if err != nil {
		if lv, lerr := lvm.Lookup(context.Background(), dev); lerr == nil && lv.Tagged() {
			_ = lvm.Remove(context.Background(), lv)
		}
		return "", fmt.Errorf("could not clear the new volume %s: %w", dev, err)
	}
	return dev, nil
}

// lvVolumeName names a standalone volume made in an LVM pool from the panel.
func lvVolumeName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(clean) > 60 {
		clean = clean[:60]
	}
	return "maburvm-vol-" + clean
}
