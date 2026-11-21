package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// CopySparse copies src into dst, a new empty file or a device that reads as
// zeroes, seeking over blocks that are all zero so a thin image stays thin on
// the destination. It returns the bytes copied; progress, when set, is told
// the running total.
func CopySparse(dst *os.File, src io.Reader, block int, progress func(int64)) (int64, error) {
	return copyBlocks(dst, src, block, true, progress)
}

// CopyAll is CopySparse writing every block, zeroes too: the destination is a
// device whose old content must not show through (a thick logical volume).
func CopyAll(dst *os.File, src io.Reader, block int, progress func(int64)) (int64, error) {
	return copyBlocks(dst, src, block, false, progress)
}

func copyBlocks(dst *os.File, src io.Reader, block int, sparse bool, progress func(int64)) (int64, error) {
	if block <= 0 {
		block = 1 << 20
	}
	buf := make([]byte, block)
	var total int64
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			if sparse && allZero(buf[:n]) {
				if _, serr := dst.Seek(int64(n), io.SeekCurrent); serr != nil {
					return total, serr
				}
			} else if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if progress != nil {
				progress(total)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return total, err
		}
	}
	// A file ending in a hole needs its length set; the seeks did not. A
	// device has its own length.
	if fi, err := dst.Stat(); err == nil && fi.Mode()&os.ModeDevice != 0 {
		return total, nil
	}
	return total, dst.Truncate(total)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// CreateImage makes an empty image of exactly size bytes at path. It refuses
// a path that exists: whatever is there belongs to something else.
func CreateImage(ctx context.Context, path, format string, size int64) error {
	if format != "qcow2" && format != "raw" {
		return fmt.Errorf("cannot create a %q image", format)
	}
	if size <= 0 {
		return fmt.Errorf("image size %d is not usable", size)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists on the destination", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := exec.CommandContext(ctx, "qemu-img", "create", "-q", "-f", format, path, strconv.FormatInt(size, 10)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img create %s: %v (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TargetPath is where a file named by the panel goes inside a pool directory.
// The name must be a plain file name: nothing the panel sends walks out of the
// pool.
func TargetPath(pool, name string) (string, error) {
	if !safePathRe.MatchString(pool) || filepath.Clean(pool) != pool || pool == "/" {
		return "", fmt.Errorf("unusable pool directory %q", pool)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("unusable file name %q", name)
	}
	return filepath.Join(pool, name), nil
}

// Prepared records the files a migration created on its destination, so a
// failed migration removes those and only those.
type Prepared struct {
	VMID        string   `json:"vm_id"`
	MigrationID string   `json:"migration_id"`
	Files       []string `json:"files"`
	// PoolType and PoolPath name an LVM pool the files (logical volumes) are in.
	PoolType string `json:"pool_type,omitempty"`
	PoolPath string `json:"pool_path,omitempty"`
}

func preparedPath(l Layout, vmID string) string {
	return filepath.Join(l.PreparedDir(), vmID+".json")
}

// SavePrepared records p, replacing any earlier record for the VM.
func SavePrepared(l Layout, p Prepared) error {
	if !ValidUUID(p.VMID) {
		return fmt.Errorf("%q is not a VM id", p.VMID)
	}
	if err := os.MkdirAll(l.PreparedDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(preparedPath(l, p.VMID), b, 0o600)
}

// LoadPrepared reads the record for a VM; false when there is none.
func LoadPrepared(l Layout, vmID string) (Prepared, bool) {
	var p Prepared
	if !ValidUUID(vmID) {
		return p, false
	}
	b, err := os.ReadFile(preparedPath(l, vmID))
	if err != nil || json.Unmarshal(b, &p) != nil || p.VMID != vmID {
		return Prepared{}, false
	}
	return p, true
}

// ClearPrepared forgets the record for a VM.
func ClearPrepared(l Layout, vmID string) {
	if ValidUUID(vmID) {
		_ = os.Remove(preparedPath(l, vmID))
	}
}

// Removable returns the recorded files that are safe to delete for a failed
// migration: recorded for this very migration, plain files (not links or
// devices), and used by no domain on the node. Recorded logical volumes are
// left to Devices.
func Removable(p Prepared, migrationID string, inUse map[string]bool) ([]string, error) {
	if p.MigrationID != migrationID {
		return nil, fmt.Errorf("the files here were prepared by another migration (%s)", p.MigrationID)
	}
	var out []string
	for _, f := range p.Files {
		f = filepath.Clean(f)
		if inUse[f] {
			return nil, fmt.Errorf("%s is in use by a VM on this node", f)
		}
		if strings.HasPrefix(f, "/dev/") {
			continue
		}
		fi, err := os.Lstat(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a plain file", f)
		}
		out = append(out, f)
	}
	return out, nil
}

// Devices are the logical volumes a migration recorded. Whoever removes one
// checks it is the VM's own first.
func Devices(p Prepared) []string {
	var out []string
	for _, f := range p.Files {
		if f = filepath.Clean(f); strings.HasPrefix(f, "/dev/") {
			out = append(out, f)
		}
	}
	return out
}
