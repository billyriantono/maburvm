package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// LVMTag is on every logical volume MaburVM creates. With the name it is the
// proof a volume is ours to remove.
const LVMTag = "maburvm"

// lvmWriteTimeout bounds writing an image onto, or zeroing, one volume.
const lvmWriteTimeout = 8 * time.Hour

// LVMPool is a registered LVM pool: a volume group, or a thin pool in one.
type LVMPool struct {
	VG   string
	Thin string
}

func (p LVMPool) String() string {
	if p.Thin != "" {
		return p.VG + "/" + p.Thin
	}
	return p.VG
}

// ParseLVMPool reads a pool the panel sent as a type and a path.
func ParseLVMPool(poolType, poolPath string) (LVMPool, error) {
	vg, thin, err := models.SplitLVMPoolPath(poolType, poolPath)
	return LVMPool{VG: vg, Thin: thin}, err
}

// LV is one logical volume as lvs reports it.
type LV struct {
	VG   string
	Name string
	Pool string // the thin pool holding it; "" for a thick volume
	Tags []string
	Size int64
	Attr string
}

// Path is the volume's device path.
func (lv LV) Path() string { return "/dev/" + lv.VG + "/" + lv.Name }

// Open reports a volume something holds open: a running VM, a mount.
func (lv LV) Open() bool { return len(lv.Attr) > 5 && lv.Attr[5] == 'o' }

// Tagged reports a volume MaburVM created.
func (lv LV) Tagged() bool {
	for _, t := range lv.Tags {
		if t == LVMTag {
			return true
		}
	}
	return false
}

// plain reports an ordinary volume: linear or striped, or a thin volume. Thin
// pools, snapshots, mirrors and the like are never a VM's disk to remove.
func (lv LV) plain() bool { return lv.Attr != "" && (lv.Attr[0] == '-' || lv.Attr[0] == 'V') }

// LVM runs the LVM tools.
type LVM struct {
	run commandRunner

	tzOnce sync.Once
	tzOK   bool
}

// NewLVM returns an LVM that runs the node's own tools.
func NewLVM() *LVM { return &LVM{run: lvmRunner{}} }

// lvmRunner runs a command with a fixed locale, so lvs prints 12.50 and not
// 12,50.
type lvmRunner struct{}

func (lvmRunner) Run(ctx context.Context, name string, args ...string) CommandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LVM_SUPPRESS_FD_WARNINGS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Err: err}
}

func (l *LVM) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	res := l.run.Run(ctx, name, args...)
	if res.Err != nil {
		return nil, fmt.Errorf("%s failed: %w (%s)", name, res.Err, strings.TrimSpace(res.StderrString()))
	}
	return res.Stdout, nil
}

const lvsFields = "vg_name,lv_name,pool_lv,lv_tags,lv_size,lv_attr"

// parseLVs reads `lvs --noheadings --nosuffix --units b --separator '|' -o lvsFields`.
func parseLVs(out []byte) ([]LV, error) {
	var lvs []LV
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != 6 {
			return nil, fmt.Errorf("unexpected lvs output: %q", line)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected lvs size %q", f[4])
		}
		lv := LV{VG: strings.TrimSpace(f[0]), Name: strings.TrimSpace(f[1]), Pool: strings.TrimSpace(f[2]), Size: size, Attr: strings.TrimSpace(f[5])}
		for _, t := range strings.Split(f[3], ",") {
			if t = strings.TrimSpace(t); t != "" {
				lv.Tags = append(lv.Tags, t)
			}
		}
		lvs = append(lvs, lv)
	}
	return lvs, nil
}

// Lookup describes the logical volume at a device path (/dev/vg/lv, its
// /dev/mapper name, or /dev/dm-N). A device that is not a logical volume is an
// error.
func (l *LVM) Lookup(ctx context.Context, dev string) (LV, error) {
	if !models.IsBlockDevicePath(dev) {
		return LV{}, fmt.Errorf("%s is not a device", dev)
	}
	dev = mapperPath(path.Clean(dev))
	out, err := l.exec(ctx, "lvs", "--noheadings", "--nosuffix", "--units", "b", "--separator", "|", "-o", lvsFields, "--", dev)
	if err != nil {
		return LV{}, fmt.Errorf("%s is not a logical volume LVM knows: %w", dev, err)
	}
	lvs, err := parseLVs(out)
	if err != nil {
		return LV{}, err
	}
	// lvs reads /dev/vg as the whole group: the one row must be dev itself.
	if len(lvs) != 1 || !lvs[0].namedBy(dev) {
		return LV{}, fmt.Errorf("%s is not one logical volume", dev)
	}
	return lvs[0], nil
}

// namedBy reports whether dev is the volume's /dev/vg/lv or /dev/mapper path.
func (lv LV) namedBy(dev string) bool {
	esc := func(s string) string { return strings.ReplaceAll(s, "-", "--") }
	return dev == lv.Path() || dev == "/dev/mapper/"+esc(lv.VG)+"-"+esc(lv.Name)
}

// mapperPath gives a /dev/dm-N node (as /proc/swaps lists one) its /dev/mapper
// name, which lvs understands; any other path is returned as it is.
func mapperPath(dev string) string {
	if path.Dir(dev) != "/dev" || !strings.HasPrefix(path.Base(dev), "dm-") {
		return dev
	}
	name, err := os.ReadFile("/sys/block/" + path.Base(dev) + "/dm/name")
	if err != nil || strings.TrimSpace(string(name)) == "" {
		return dev
	}
	return "/dev/mapper/" + strings.TrimSpace(string(name))
}

// List returns every volume in a volume group.
func (l *LVM) List(ctx context.Context, vg string) ([]LV, error) {
	if !models.ValidLVMName(vg) {
		return nil, fmt.Errorf("%q is not a volume group name", vg)
	}
	out, err := l.exec(ctx, "lvs", "--noheadings", "--nosuffix", "--units", "b", "--separator", "|", "-o", lvsFields, "--", vg)
	if err != nil {
		return nil, err
	}
	return parseLVs(out)
}

// FreeName returns name when the pool's volume group has no volume called
// that, else name with a short unique tag in place of its suffix (a disk
// moving within one group keeps its old volume until the copy is done).
func (l *LVM) FreeName(ctx context.Context, pool LVMPool, vmID, suffix string) (string, error) {
	name := models.LVMDiskName(vmID, suffix)
	if _, exists, err := l.find(ctx, pool, name); err != nil || !exists {
		return name, err
	}
	return models.LVMDiskName(vmID, "m"+strconv.FormatInt(time.Now().UnixNano(), 36)), nil
}

// find returns the volume called name in the pool's volume group, if any.
func (l *LVM) find(ctx context.Context, pool LVMPool, name string) (LV, bool, error) {
	lvs, err := l.List(ctx, pool.VG)
	if err != nil {
		return LV{}, false, err
	}
	for _, lv := range lvs {
		if lv.Name == name {
			return lv, true, nil
		}
	}
	return LV{}, false, nil
}

// HeldBy returns the registered pool holding the volume. A thick
// registration of a volume group covers every volume in it.
func HeldBy(lv LV, pools []LVMPool) (LVMPool, bool) {
	var group *LVMPool
	for i, p := range pools {
		if p.VG != lv.VG {
			continue
		}
		if p.Thin != "" && p.Thin == lv.Pool {
			return p, true
		}
		if p.Thin == "" {
			group = &pools[i]
		}
	}
	if group != nil {
		return *group, true
	}
	return LVMPool{}, false
}

// Owned reports a volume MaburVM created for the VM: its name and its tag.
func Owned(lv LV, vmID string) bool { return lv.Tagged() && models.OwnsLVMName(vmID, lv.Name) }

// CheckOwnership says why the volume is not one to remove, or nil: it must be
// an ordinary volume in a registered pool, and one MaburVM created for vmID
// unless foreign volumes are allowed.
func CheckOwnership(lv LV, vmID string, pools []LVMPool, allowForeign bool) error {
	if _, ok := HeldBy(lv, pools); !ok {
		return fmt.Errorf("%s is not in a storage pool registered with the panel", lv.Path())
	}
	if !lv.plain() {
		return fmt.Errorf("%s is not an ordinary logical volume (attributes %s)", lv.Path(), lv.Attr)
	}
	if !allowForeign && !Owned(lv, vmID) {
		return fmt.Errorf("%s was not created by MaburVM for this VM", lv.Path())
	}
	return nil
}

// CheckRemovable is CheckOwnership for a volume nothing holds open.
func CheckRemovable(lv LV, vmID string, pools []LVMPool, allowForeign bool) error {
	if err := CheckOwnership(lv, vmID, pools, allowForeign); err != nil {
		return err
	}
	if lv.Open() {
		return fmt.Errorf("%s is in use", lv.Path())
	}
	return nil
}

// Create makes a tagged volume of at least size bytes in the pool.
func (l *LVM) Create(ctx context.Context, pool LVMPool, name string, size int64) (string, error) {
	if !models.ValidLVMName(name) || !models.ValidLVMName(pool.VG) || (pool.Thin != "" && !models.ValidLVMName(pool.Thin)) {
		return "", fmt.Errorf("invalid volume %q in pool %q", name, pool)
	}
	if size <= 0 {
		return "", fmt.Errorf("volume size must be positive")
	}
	args := []string{"--yes", "--name", name, "--addtag", LVMTag}
	if pool.Thin != "" {
		args = append(args, "--type", "thin", "--virtualsize", fmt.Sprintf("%db", size), "--thinpool", pool.Thin)
	} else {
		args = append(args, "--size", fmt.Sprintf("%db", size), "--wipesignatures", "y")
	}
	if _, err := l.exec(ctx, "lvcreate", append(args, pool.VG)...); err != nil {
		return "", err
	}
	return "/dev/" + pool.VG + "/" + name, nil
}

// Extend grows the volume to size bytes; a volume already that large is left.
func (l *LVM) Extend(ctx context.Context, lv LV, size int64) error {
	if size <= lv.Size {
		return nil
	}
	_, err := l.exec(ctx, "lvextend", "--size", fmt.Sprintf("%db", size), "--", lv.VG+"/"+lv.Name)
	return err
}

// Remove deletes the volume. Callers check CheckRemovable first.
func (l *LVM) Remove(ctx context.Context, lv LV) error {
	_, err := l.exec(ctx, "lvremove", "--yes", "--force", "--", lv.VG+"/"+lv.Name)
	return err
}

// LVMCapacity is what a pool holds: a volume group's extents, or a thin
// pool's data, plus its metadata use.
type LVMCapacity struct {
	Total, Used, Available int64
	MetadataPercent        float64
}

// Capacity measures the pool.
func (l *LVM) Capacity(ctx context.Context, pool LVMPool) (LVMCapacity, error) {
	if pool.Thin == "" {
		out, err := l.exec(ctx, "vgs", "--noheadings", "--nosuffix", "--units", "b", "--separator", "|", "-o", "vg_size,vg_free", "--", pool.VG)
		if err != nil {
			return LVMCapacity{}, fmt.Errorf("volume group %s: %w", pool.VG, err)
		}
		return parseVGCapacity(out)
	}
	out, err := l.exec(ctx, "lvs", "--noheadings", "--nosuffix", "--units", "b", "--separator", "|", "-o", "lv_size,data_percent,metadata_percent,lv_attr", "--", pool.String())
	if err != nil {
		return LVMCapacity{}, fmt.Errorf("thin pool %s: %w", pool, err)
	}
	return parseThinCapacity(pool, out)
}

func parseVGCapacity(out []byte) (LVMCapacity, error) {
	f := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(f) != 2 {
		return LVMCapacity{}, fmt.Errorf("unexpected vgs output: %q", out)
	}
	total, err1 := strconv.ParseInt(strings.TrimSpace(f[0]), 10, 64)
	free, err2 := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
	if err := errors.Join(err1, err2); err != nil || total <= 0 {
		return LVMCapacity{}, fmt.Errorf("unexpected vgs output: %q", out)
	}
	return LVMCapacity{Total: total, Used: total - free, Available: free}, nil
}

func parseThinCapacity(pool LVMPool, out []byte) (LVMCapacity, error) {
	f := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(f) != 4 {
		return LVMCapacity{}, fmt.Errorf("unexpected lvs output: %q", out)
	}
	if attr := strings.TrimSpace(f[3]); attr == "" || attr[0] != 't' {
		return LVMCapacity{}, fmt.Errorf("%s is not a thin pool", pool)
	}
	size, err1 := strconv.ParseInt(strings.TrimSpace(f[0]), 10, 64)
	data, err2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
	meta, err3 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
	if err := errors.Join(err1, err2, err3); err != nil || size <= 0 {
		return LVMCapacity{}, fmt.Errorf("unexpected lvs output: %q", out)
	}
	used := int64(float64(size) * data / 100)
	return LVMCapacity{Total: size, Used: used, Available: size - used, MetadataPercent: meta}, nil
}

// hostMounts are the mount points of the host's own system. A volume group
// holding any of them, or a swap device, is not given to VMs.
var hostMounts = map[string]bool{"/": true, "/boot": true, "/usr": true, "/var": true}

// hostDevices lists the devices behind the host's system mounts and swap,
// from /proc/mounts and /proc/swaps.
func hostDevices(mounts, swaps []byte) []string {
	var out []string
	for _, line := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && hostMounts[f[1]] && strings.HasPrefix(f[0], "/dev/") {
			out = append(out, f[0])
		}
	}
	for i, line := range strings.Split(string(swaps), "\n") {
		f := strings.Fields(line)
		if i > 0 && len(f) >= 1 && strings.HasPrefix(f[0], "/dev/") {
			out = append(out, f[0])
		}
	}
	return out
}

// Check says why the pool cannot hold VM disks, or nil: it must exist, a thin
// pool must be one, and it must not hold the host's system.
func (l *LVM) Check(ctx context.Context, pool LVMPool) error {
	if _, err := l.Capacity(ctx, pool); err != nil {
		return err
	}
	mounts, _ := os.ReadFile("/proc/mounts")
	swaps, _ := os.ReadFile("/proc/swaps")
	return l.checkHost(ctx, pool, hostDevices(mounts, swaps))
}

// checkHost refuses a pool holding one of the host's system devices. A thin
// pool only shares its volume group with them: its volumes take no extents
// the host's own volumes could need.
func (l *LVM) checkHost(ctx context.Context, pool LVMPool, devs []string) error {
	for _, dev := range devs {
		lv, err := l.Lookup(ctx, dev)
		if err == nil && lv.VG == pool.VG && (pool.Thin == "" || lv.Pool == pool.Thin) {
			return fmt.Errorf("%s holds this host's own system (%s); give VMs a volume group or thin pool of their own", pool, dev)
		}
	}
	return nil
}

// imageSize is the virtual size of the image at src, read as format when one
// is given. A raw device is measured, not probed.
func (l *LVM) imageSize(ctx context.Context, src, format string) (int64, error) {
	if format == "raw" && models.IsBlockDevicePath(src) {
		return DeviceSize(src)
	}
	args := []string{"info", "-U", "--output=json"}
	if format != "" {
		args = append(args, "-f", format)
	}
	out, err := l.exec(ctx, "qemu-img", append(args, src)...)
	if err != nil {
		return 0, err
	}
	info, err := parseImageInfo(out)
	if err != nil {
		return 0, err
	}
	return info.VirtualSize, nil
}

// DeviceSize is a block device's (or file's) size in bytes.
func DeviceSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Seek(0, io.SeekEnd)
}

// targetIsZeroSupported reports a qemu-img (5.0 or later) that can be told a
// new thin volume already reads as zeroes, so it does not write them.
func (l *LVM) targetIsZeroSupported(ctx context.Context) bool {
	l.tzOnce.Do(func() {
		res := l.run.Run(ctx, "qemu-img", "--help")
		l.tzOK = bytes.Contains(res.Stdout, []byte("--target-is-zero"))
	})
	return l.tzOK
}

// WriteImage writes the image at src (read as format; "" probes, which only a
// trusted template may be) onto the device. zeroed says the device already
// reads as zeroes, as a new thin volume does.
func (l *LVM) WriteImage(ctx context.Context, src, format, dev string, zeroed bool) error {
	args := []string{"convert", "-n"}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, "-O", "raw")
	if zeroed && l.targetIsZeroSupported(ctx) {
		args = append(args, "--target-is-zero")
	}
	ctx, cancel := context.WithTimeout(ctx, lvmWriteTimeout)
	defer cancel()
	_, err := l.exec(ctx, "qemu-img", append(args, src, dev)...)
	return err
}

// zeroAlign is where blkdiscard takes over from plain writes.
const zeroAlign = 1 << 20

// ZeroRange zeroes [from, to) of the device, so a new thick volume does not
// hand its VM what an earlier one left on the same extents.
func (l *LVM) ZeroRange(ctx context.Context, dev string, from, to int64) error {
	if from >= to {
		return nil
	}
	head := min((from+zeroAlign-1)/zeroAlign*zeroAlign, to)
	if head > from {
		if err := writeZeroes(dev, from, head); err != nil {
			return err
		}
	}
	if head >= to {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, lvmWriteTimeout)
	defer cancel()
	_, err := l.exec(ctx, "blkdiscard", "--zeroout", "--offset", strconv.FormatInt(head, 10), "--length", strconv.FormatInt(to-head, 10), dev)
	return err
}

func writeZeroes(path string, from, to int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(make([]byte, to-from), from); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Provision creates the volume name in the pool and fills it with the image at
// src (format as WriteImage), or leaves it blank when src is empty. It is at
// least size bytes and never smaller than the image. A thick volume is zeroed
// wherever the image does not reach. A failure leaves no volume behind.
func (l *LVM) Provision(ctx context.Context, pool LVMPool, name string, size int64, src, format string) (dev string, err error) {
	var imgSize int64
	if src != "" {
		if imgSize, err = l.imageSize(ctx, src, format); err != nil {
			return "", fmt.Errorf("could not read the image %s: %w", src, err)
		}
		size = max(size, imgSize)
	}
	if _, exists, ferr := l.find(ctx, pool, name); ferr != nil {
		return "", ferr
	} else if exists {
		return "", fmt.Errorf("logical volume %s/%s already exists", pool.VG, name)
	}
	if dev, err = l.Create(ctx, pool, name, size); err != nil {
		return "", err
	}
	defer func() {
		if err == nil {
			return
		}
		if lv, ok, ferr := l.find(context.Background(), pool, name); ferr == nil && ok && lv.Tagged() {
			if rerr := l.Remove(context.Background(), lv); rerr != nil {
				err = fmt.Errorf("%w; and the new volume %s could not be removed: %v", err, dev, rerr)
			}
		}
	}()
	if src != "" {
		if err = l.WriteImage(ctx, src, format, dev, pool.Thin != ""); err != nil {
			return "", err
		}
	}
	if pool.Thin == "" {
		end, serr := DeviceSize(dev)
		if serr != nil {
			return "", serr
		}
		if err = l.ZeroRange(ctx, dev, imgSize, end); err != nil {
			return "", fmt.Errorf("could not clear the new volume: %w", err)
		}
	}
	return dev, nil
}
