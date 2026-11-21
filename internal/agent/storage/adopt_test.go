package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

// A recovered VM is only defined on a disk in a mounted shared pool, and never
// while a backup overlay of that disk is lying beside it.
func TestCheckAdoptDiskRefusesWhatIsNotSafe(t *testing.T) {
	prev := SharedPoolsFile
	t.Cleanup(func() { SharedPoolsFile = prev })
	dir := t.TempDir()
	SharedPoolsFile = filepath.Join(dir, "shared-pools.json")
	pool := filepath.Join(dir, "pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	const vm, poolID = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	disk := filepath.Join(pool, vm+".qcow2")
	if err := os.WriteFile(disk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := func(part string) {
		t.Helper()
		if err := CheckAdoptDisk(vm, "vda", disk); err == nil || !strings.Contains(err.Error(), part) {
			t.Fatalf("want an error with %q, got %v", part, err)
		}
	}

	want("not in a shared pool")
	if err := SaveSharedPool(SharedPool{ID: poolID, Kind: models.SharedPoolMounted, Path: pool}); err != nil {
		t.Fatal(err)
	}
	want("not mounted")
	if err := os.WriteFile(filepath.Join(pool, models.SharedPoolMarker), []byte(poolID), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(pool, vm+"-vda"+OverlayMarker+"1700000000.qcow2")
	if err := os.WriteFile(overlay, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want("latest writes")
	if err := os.Remove(disk); err != nil {
		t.Fatal(err)
	}
	want("is not at")
}

// A disk built on an image outside the shared pool is not adopted: the node
// would open its own file at that path. One built on an image beside it is.
func TestCheckAdoptDiskNeedsItsBackingImageInThePool(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img is not installed")
	}
	pool := sharedTestPool(t)
	if err := os.WriteFile(filepath.Join(pool, models.SharedPoolMarker), []byte(testPoolID), 0o600); err != nil {
		t.Fatal(err)
	}
	img := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("qemu-img", append([]string{"create", "-q", "-f", "qcow2"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("qemu-img: %v: %s", err, out)
		}
	}
	outside := filepath.Join(t.TempDir(), "template.qcow2")
	img(outside, "1M")
	img(filepath.Join(pool, "template.qcow2"), "1M")
	const vm = "33333333-3333-3333-3333-333333333333"
	disk := filepath.Join(pool, vm+".qcow2")

	img("-F", "qcow2", "-b", outside, disk)
	if err := CheckAdoptDisk(vm, "vda", disk); err == nil || !strings.Contains(err.Error(), "not in the shared pool") {
		t.Fatalf("a disk on a backing image outside the pool must be refused, got %v", err)
	}
	if err := os.Remove(disk); err != nil {
		t.Fatal(err)
	}
	img("-F", "qcow2", "-b", "template.qcow2", disk)
	if err := CheckAdoptDisk(vm, "vda", disk); err != nil {
		t.Fatalf("a disk on a backing image beside it is adopted: %v", err)
	}
}

func TestLockRefused(t *testing.T) {
	held := `qemu-img: Could not open '/p/a.qcow2': Failed to get shared "write" lock
Is another process using the image [/p/a.qcow2]?`
	if !lockRefused(held) {
		t.Fatal("an image a running VM holds is refused for its lock")
	}
	if lockRefused("qemu-img: Could not open '/p/a.qcow2': No such file or directory") {
		t.Fatal("a missing file is not a held lock")
	}
}
