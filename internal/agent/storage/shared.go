package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirtxml"
)

// Shared pools: storage this node shares with other nodes at the same path.
// The panel says which paths are shared (SaveSharedPool); from then on nothing
// is written under one unless its marker file is there, and disks defined in
// one bypass the host cache.

// SharedPool is a shared pool this node is a member of.
type SharedPool struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// SharedPoolsFile lists the shared pools this node is a member of. The agent
// points it into its data directory at start.
var SharedPoolsFile = "/var/lib/maburvm/shared-pools.json"

var sharedPoolsMu sync.Mutex

// SharedPools reads the list. A missing file is no pools.
//
// ponytail: read from disk on every call (a few hundred bytes). Cache it by
// mtime if defining disks ever gets hot.
func SharedPools() []SharedPool {
	raw, err := os.ReadFile(SharedPoolsFile)
	if err != nil {
		return nil
	}
	var out []SharedPool
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func writeSharedPools(pools []SharedPool) error {
	raw, err := json.MarshalIndent(pools, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(SharedPoolsFile), 0o755); err != nil {
		return err
	}
	tmp := SharedPoolsFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, SharedPoolsFile)
}

// SaveSharedPool records a pool, replacing an earlier record of the same id.
func SaveSharedPool(p SharedPool) error {
	sharedPoolsMu.Lock()
	defer sharedPoolsMu.Unlock()
	p.Path = filepath.Clean(p.Path)
	var out []SharedPool
	for _, o := range SharedPools() {
		if o.ID != p.ID {
			out = append(out, o)
		}
	}
	return writeSharedPools(append(out, p))
}

// RemoveSharedPool forgets a pool.
func RemoveSharedPool(id string) error {
	sharedPoolsMu.Lock()
	defer sharedPoolsMu.Unlock()
	var out []SharedPool
	for _, o := range SharedPools() {
		if o.ID != id {
			out = append(out, o)
		}
	}
	return writeSharedPools(out)
}

// SharedPoolAt is the shared pool holding p: its directory, or a path in it.
func SharedPoolAt(p string) (SharedPool, bool) {
	p = filepath.Clean(p)
	for _, o := range SharedPools() {
		if p == o.Path || strings.HasPrefix(p, o.Path+"/") {
			return o, true
		}
	}
	return SharedPool{}, false
}

// CheckSharedDir refuses a directory in a shared pool that is not the shared
// storage right now: the pool's marker file is missing or names another pool.
// That is what an unmounted path looks like, and a disk written there would
// land on the node's own disk underneath. Any other directory passes.
func CheckSharedDir(dir string) error {
	pool, ok := SharedPoolAt(dir)
	if !ok {
		return nil
	}
	got, err := os.ReadFile(filepath.Join(pool.Path, models.SharedPoolMarker))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("the shared storage is not mounted at %s on this node (its marker file is missing); nothing is written to the local disk underneath", pool.Path)
	case err != nil:
		return fmt.Errorf("the shared storage at %s cannot be read on this node: %v", pool.Path, err)
	case strings.TrimSpace(string(got)) != pool.ID:
		return fmt.Errorf("%s on this node holds another shared pool's marker; nothing is written there", pool.Path)
	}
	return nil
}

var sharedTokenNameRe = regexp.MustCompile(`^\.maburvm-[a-z0-9-]{1,80}$`)

// maxSharedToken bounds a token file: a pool id or a short random string.
const maxSharedToken = 256

// SharedToken writes, reads or removes one small file directly under dir. A
// write never replaces a file: it reports the one that is there.
func SharedToken(dir, name, op, content string) (got string, exists bool, err error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", false, fmt.Errorf("%q is not an absolute directory path", dir)
	}
	if !sharedTokenNameRe.MatchString(name) || len(content) > maxSharedToken {
		return "", false, fmt.Errorf("unusable token file")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false, fmt.Errorf("the directory %s does not exist on this node", dir)
	}
	file := filepath.Join(dir, name)
	read := func() (string, bool, error) {
		f, err := os.Open(file)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxSharedToken))
		return strings.TrimSpace(string(b)), true, err
	}
	switch op {
	case "read":
		return read()
	case "remove":
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		return "", false, nil
	case "write":
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			return read()
		}
		if err != nil {
			return "", false, fmt.Errorf("%s is not writable on this node: %w", dir, err)
		}
		_, err = f.WriteString(content)
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(file)
			return "", false, err
		}
		return content, true, nil
	}
	return "", false, fmt.Errorf("unknown token operation %q", op)
}

// IsMountPoint reports a directory that is the root of a mounted filesystem:
// on another device than its parent.
func IsMountPoint(dir string) bool {
	a, err := os.Stat(dir)
	if err != nil {
		return false
	}
	b, err := os.Stat(filepath.Dir(dir))
	if err != nil {
		return false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev != sb.Dev
}

// OnRootFilesystem reports a directory on the same filesystem as "/": the
// node's own disk, where shared storage cannot be.
func OnRootFilesystem(dir string) bool {
	a, err := os.Stat(dir)
	if err != nil {
		return false
	}
	b, err := os.Stat("/")
	if err != nil {
		return false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev == sb.Dev
}

// systemdUnitDir is where the agent keeps the mount units of nfs pools.
var systemdUnitDir = "/etc/systemd/system"

// MountUnitName is the systemd unit that mounts at dir (systemd-escape
// --path --suffix=mount): the unit of a mount point must carry its name.
func MountUnitName(dir string) string {
	var b strings.Builder
	for i, part := range strings.Split(strings.Trim(filepath.Clean(dir), "/"), "/") {
		if i > 0 {
			b.WriteByte('-')
		}
		for j := 0; j < len(part); j++ {
			c := part[j]
			plain := c == '_' || c == ':' || (c == '.' && j > 0) ||
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if plain {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		}
	}
	return b.String() + ".mount"
}

// defaultNFSOptions keep a VM's writes from being dropped: a hard mount waits
// for the server instead of returning errors to the guest.
const defaultNFSOptions = "hard"

// NFSMountUnit is the unit that mounts an nfs pool at boot, before libvirt
// starts VMs whose disks are on it.
func NFSMountUnit(poolID, server, export, options, where string) string {
	if options == "" {
		options = defaultNFSOptions
	} else if !strings.Contains(","+options+",", ",hard,") {
		options = defaultNFSOptions + "," + options
	}
	return "# Written by the MaburVM agent for shared pool " + poolID + ". Remove the node from the pool in the panel to remove it.\n" +
		"[Unit]\n" +
		"Description=MaburVM shared pool " + poolID + "\n" +
		"Wants=network-online.target\n" +
		"After=network-online.target\n" +
		"Before=libvirtd.service virtqemud.service libvirt-guests.service\n\n" +
		"[Mount]\n" +
		"What=" + server + ":" + export + "\n" +
		"Where=" + where + "\n" +
		"Type=nfs\n" +
		"Options=" + options + "\n" +
		"TimeoutSec=60\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

func systemctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// MountNFSPool mounts an nfs pool at its fixed path with a systemd mount
// unit, so it is mounted again after a reboot. A mount that fails leaves no
// unit behind. Values are checked by models.ValidateSharedPool first.
func MountNFSPool(ctx context.Context, poolID, server, export, options string) (string, error) {
	where, err := models.ValidateSharedPool(poolID, models.SharedPoolNFS, "", server, export, options)
	if err != nil {
		return "", err
	}
	if _, err := exec.LookPath("mount.nfs"); err != nil {
		return "", fmt.Errorf("the NFS client is not installed on this node (no mount.nfs); install nfs-common (nfs-utils on the RHEL family)")
	}
	if err := os.MkdirAll(where, 0o755); err != nil {
		return "", err
	}
	unit := MountUnitName(where)
	file := filepath.Join(systemdUnitDir, unit)
	want := NFSMountUnit(poolID, server, export, options, where)
	if have, err := os.ReadFile(file); err == nil && string(have) == want && IsMountPoint(where) {
		return where, nil
	}
	if IsMountPoint(where) {
		return "", fmt.Errorf("%s is already mounted with other settings; remove the node from the pool first", where)
	}
	if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
		return "", err
	}
	err = systemctl(ctx, "daemon-reload")
	if err == nil {
		err = systemctl(ctx, "enable", "--now", unit)
	}
	if err == nil && !IsMountPoint(where) {
		err = fmt.Errorf("%s is not a mount point after mounting", where)
	}
	if err != nil {
		_ = systemctl(context.WithoutCancel(ctx), "disable", "--now", unit)
		_ = os.Remove(file)
		_ = systemctl(context.WithoutCancel(ctx), "daemon-reload")
		return "", fmt.Errorf("%s:%s could not be mounted at %s: %w (the node's journal has the mount error: journalctl -u '%s')", server, export, where, err, unit)
	}
	return where, nil
}

// UnmountNFSPool unmounts an nfs pool and removes its unit. The caller has
// made sure no VM here uses it.
func UnmountNFSPool(ctx context.Context, poolID string) error {
	where := models.SharedPoolMountPath(poolID)
	unit := MountUnitName(where)
	file := filepath.Join(systemdUnitDir, unit)
	if _, err := os.Stat(file); err == nil {
		if err := systemctl(ctx, "disable", "--now", unit); err != nil && IsMountPoint(where) {
			return err
		}
		if err := os.Remove(file); err != nil {
			return err
		}
		_ = systemctl(ctx, "daemon-reload")
	}
	if IsMountPoint(where) {
		return fmt.Errorf("%s is still mounted", where)
	}
	// Only an empty directory goes: anything in it was written while unmounted.
	_ = os.Remove(where)
	return nil
}

// sharedDiskDriver makes a file disk safe on shared storage: the host page
// cache is bypassed, so a VM moved to another node without copying its disk
// finds every write there. QEMU's image locking stays on (it is the default),
// which keeps a second QEMU from writing the same image.
func sharedDiskDriver(d *libvirtxml.DomainDisk) bool {
	if d.Device == "cdrom" || d.ReadOnly != nil || d.Source == nil || d.Source.File == nil || d.Driver == nil {
		return false
	}
	if _, ok := SharedPoolAt(d.Source.File.File); !ok || d.Driver.Cache == "none" || d.Driver.Cache == "directsync" {
		return false
	}
	d.Driver.Cache = "none"
	return true
}

// TuneSharedDisks returns the domain with every disk in a shared pool set to
// bypass the host cache, and whether anything changed.
func TuneSharedDisks(domXML string) (string, bool, error) {
	if len(SharedPools()) == 0 {
		return domXML, false, nil
	}
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return "", false, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices == nil {
		return domXML, false, nil
	}
	changed := false
	for i := range dom.Devices.Disks {
		if sharedDiskDriver(&dom.Devices.Disks[i]) {
			changed = true
		}
	}
	if !changed {
		return domXML, false, nil
	}
	out, err := dom.Marshal()
	return out, true, err
}
