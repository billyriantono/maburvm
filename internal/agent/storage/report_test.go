package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of this function is that it measures the path it is given.
// The bug it replaces measured "/" for every pool, so a pool directory on a
// different mount reported the root filesystem's numbers.
func TestCapacityMeasuresTheGivenPath(t *testing.T) {
	dir := t.TempDir()

	got := Capacity(dir)

	if !got.Exists {
		t.Fatalf("temp dir should exist, got %+v", got)
	}
	if got.Total <= 0 {
		t.Errorf("total should be positive, got %d", got.Total)
	}
	if got.Available < 0 || got.Available > got.Total {
		t.Errorf("available %d is not within 0..total %d", got.Available, got.Total)
	}
	if got.Used < 0 || got.Used > got.Total {
		t.Errorf("used %d is not within 0..total %d", got.Used, got.Total)
	}
	// Used counts root-reserved blocks, so used + available is normally a little
	// under total. It must never exceed it.
	if got.Used+got.Available > got.Total {
		t.Errorf("used %d + available %d exceeds total %d", got.Used, got.Available, got.Total)
	}
	if got.Path != dir {
		t.Errorf("path should be echoed back, got %q want %q", got.Path, dir)
	}
}

// A pool pointing somewhere that is not on this node must be reported as
// missing. Zero bytes used on an existing path reads as a healthy empty pool,
// which would invite an operator to place VMs on storage that is not there.
func TestCapacityMissingPathIsNotAnEmptyPool(t *testing.T) {
	got := Capacity(filepath.Join(t.TempDir(), "definitely-not-here"))

	if got.Exists {
		t.Error("a missing path must not report as existing")
	}
	if got.Total != 0 || got.Used != 0 || got.Available != 0 {
		t.Errorf("a missing path must report no capacity, got %+v", got)
	}
}

func TestCapacityEmptyPath(t *testing.T) {
	if got := Capacity(""); got.Exists {
		t.Errorf("empty path must not report as existing, got %+v", got)
	}
}

// The mount point is what lets the UI say "these two pools share one
// filesystem" instead of adding their free space together and promising twice
// what exists.
func TestMountPointIsAnAncestor(t *testing.T) {
	dir := t.TempDir()

	mount := mountPointOf(dir)

	if mount == "" {
		t.Fatal("expected a mount point")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs, mount) {
		t.Errorf("mount point %q is not an ancestor of %q", mount, abs)
	}
}
