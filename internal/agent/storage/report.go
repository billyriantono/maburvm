package storage

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// PoolCapacity is one pool path's real filesystem usage.
type PoolCapacity struct {
	Path      string
	Exists    bool
	Total     int64
	Used      int64
	Available int64
	// Filesystem is the mount point backing Path, so two pools sharing one
	// filesystem can be shown as sharing it rather than as capacity that adds up.
	Filesystem string
}

// Capacity measures the filesystem behind a pool path.
//
// It measures the given path, not the root filesystem. That sounds obvious, and
// the bug it fixes was exactly that: every pool was reported with the node's `/`
// usage, so a pool directory containing nothing showed gigabytes in use while
// the separate volume holding every customer's disk — 76% full — was invisible.
//
// Used is computed from total minus FREE (not minus available), so the blocks
// reserved for root are counted as used. Reporting them as free promises space
// that a VM writing as a normal user cannot actually have.
func Capacity(path string) PoolCapacity {
	out := PoolCapacity{Path: path}
	if path == "" {
		return out
	}
	if _, err := os.Stat(path); err != nil {
		// A pool pointing at a path that is not on this node is a
		// misconfiguration. Report it as missing rather than as an empty pool,
		// which is what zero bytes used would look like.
		return out
	}
	out.Exists = true

	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return out
	}
	bsize := int64(st.Bsize)
	out.Total = int64(st.Blocks) * bsize
	out.Available = int64(st.Bavail) * bsize
	out.Used = out.Total - int64(st.Bfree)*bsize
	out.Filesystem = mountPointOf(path)
	return out
}

// mountPointOf walks up until the filesystem changes, which is the mount point
// the path lives on. Done by device id rather than by parsing /proc/mounts:
// bind mounts, overlays and symlinked pool paths all make the textual answer
// disagree with the one statfs actually measured.
func mountPointOf(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	var st unix.Stat_t
	if err := unix.Stat(abs, &st); err != nil {
		return ""
	}
	dev := st.Dev

	current := abs
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return current // reached "/"
		}
		var pst unix.Stat_t
		if err := unix.Stat(parent, &pst); err != nil || pst.Dev != dev {
			return current
		}
		current = parent
	}
}
