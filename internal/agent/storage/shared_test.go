package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

const testPoolID = "0b6f1c3e-8a52-4a0f-9d0e-1f2a3b4c5d6e"

// sharedTestPool registers a shared pool at a fresh directory.
func sharedTestPool(t *testing.T) string {
	t.Helper()
	old := SharedPoolsFile
	SharedPoolsFile = filepath.Join(t.TempDir(), "shared-pools.json")
	t.Cleanup(func() { SharedPoolsFile = old })
	dir := filepath.Join(t.TempDir(), "pool")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveSharedPool(SharedPool{ID: testPoolID, Kind: models.SharedPoolMounted, Path: dir}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An unmounted shared pool is the local directory underneath: no marker, and
// nothing may be written there.
func TestSharedPoolWithoutItsMarkerTakesNoDisks(t *testing.T) {
	dir := sharedTestPool(t)
	if err := CheckSharedDir(dir); err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("a pool without its marker passed: %v", err)
	}
	if _, err := ResolvePoolDir(dir, "/default"); err == nil {
		t.Fatal("a new disk was allowed into an unmounted shared pool")
	}
	if _, _, err := NewVolumeManager().CreateVolume("dir", dir, "vol", "qcow2", 1); err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("a volume was allowed into an unmounted shared pool: %v", err)
	}
	if c := Capacity(dir); c.Problem == "" || c.Total != 0 {
		t.Fatalf("the local disk underneath was measured as the pool: %+v", c)
	}

	// Another pool's storage at the path is not this pool either.
	if _, _, err := SharedToken(dir, models.SharedPoolMarker, "write", "some-other-pool"); err != nil {
		t.Fatal(err)
	}
	if err := CheckSharedDir(dir); err == nil {
		t.Fatal("another pool's marker passed")
	}
	if _, _, err := SharedToken(dir, models.SharedPoolMarker, "remove", ""); err != nil {
		t.Fatal(err)
	}

	if _, _, err := SharedToken(dir, models.SharedPoolMarker, "write", testPoolID); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolvePoolDir(dir, "/default"); err != nil || got != dir {
		t.Fatalf("a mounted shared pool was refused: %q %v", got, err)
	}
	if c := Capacity(dir); c.Problem != "" || c.Total == 0 {
		t.Fatalf("a mounted shared pool was not measured: %+v", c)
	}
	// A directory that is no shared pool is not this check's business.
	if err := CheckSharedDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if err := RemoveSharedPool(testPoolID); err != nil {
		t.Fatal(err)
	}
	if _, ok := SharedPoolAt(dir); ok {
		t.Fatal("the pool is still registered after it was removed")
	}
}

func TestSharedTokenNeverReplacesAndStaysInItsDirectory(t *testing.T) {
	dir := t.TempDir()
	if got, exists, err := SharedToken(dir, ".maburvm-probe-1", "write", "first"); err != nil || !exists || got != "first" {
		t.Fatalf("write: %q %v %v", got, exists, err)
	}
	if got, _, _ := SharedToken(dir, ".maburvm-probe-1", "write", "second"); got != "first" {
		t.Fatalf("a write replaced the token that was there: %q", got)
	}
	if _, exists, _ := SharedToken(dir, ".maburvm-probe-2", "read", ""); exists {
		t.Fatal("a token nobody wrote was read")
	}
	for _, name := range []string{"../.maburvm-x", ".maburvm-/x", "disk.qcow2", ".maburvm-", ".maburvm-a b"} {
		if _, _, err := SharedToken(dir, name, "write", "x"); err == nil {
			t.Errorf("token name %q was accepted", name)
		}
	}
	if _, _, err := SharedToken(dir, ".maburvm-big", "write", strings.Repeat("x", 300)); err == nil {
		t.Error("an oversized token was accepted")
	}
}

// Disks in a shared pool bypass the host cache; QEMU's image locking is left
// at its default (on), so no element turns it off.
func TestDisksInASharedPoolBypassTheHostCache(t *testing.T) {
	dir := sharedTestPool(t)
	if d := DiskDevice(filepath.Join(dir, "a.qcow2"), "vda"); d.Driver.Cache != "none" {
		t.Fatalf("cache = %q for a disk in a shared pool", d.Driver.Cache)
	}
	if d := DiskDevice("/var/lib/libvirt/images/a.qcow2", "vda"); d.Driver.Cache != "" {
		t.Fatalf("cache = %q for a local disk; it was left alone before", d.Driver.Cache)
	}
	in := `<domain type="kvm"><name>x</name><devices>
		<disk type="file" device="disk"><driver name="qemu" type="qcow2"/><source file="` + dir + `/a.qcow2"/><target dev="vda" bus="virtio"/></disk>
		<disk type="file" device="disk"><driver name="qemu" type="qcow2" cache="writeback"/><source file="/data/b.qcow2"/><target dev="vdb" bus="virtio"/></disk>
		<disk type="file" device="cdrom"><driver name="qemu" type="raw"/><source file="` + dir + `/a-seed.iso"/><target dev="sda" bus="sata"/><readonly/></disk>
	</devices></domain>`
	out, changed, err := TuneSharedDisks(in)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Count(out, `cache="none"`) != 1 || !strings.Contains(out, `cache="writeback"`) {
		t.Fatalf("only the shared disk should change:\n%s", out)
	}
	if strings.Contains(out, "locking") {
		t.Fatalf("image locking must be left on:\n%s", out)
	}
	if _, changed, _ := TuneSharedDisks(out); changed {
		t.Fatal("tuning twice changed the domain again")
	}
}

func TestNFSMountUnit(t *testing.T) {
	where := models.SharedPoolMountPath(testPoolID)
	// systemd-escape --path --suffix=mount gives the same.
	want := `var-lib-maburvm-pools-0b6f1c3e\x2d8a52\x2d4a0f\x2d9d0e\x2d1f2a3b4c5d6e.mount`
	if got := MountUnitName(where); got != want {
		t.Fatalf("unit name %q, want %q", got, want)
	}
	unit := NFSMountUnit(testPoolID, "nas.example.net", "/export/vms", "vers=4.2,noatime", where)
	for _, line := range []string{"What=nas.example.net:/export/vms", "Where=" + where, "Type=nfs", "Options=hard,vers=4.2,noatime", "WantedBy=multi-user.target", "Before=libvirtd.service"} {
		if !strings.Contains(unit, line) {
			t.Errorf("the unit lacks %q:\n%s", line, unit)
		}
	}
	if !strings.Contains(NFSMountUnit(testPoolID, "nas", "/x", "", where), "Options=hard\n") {
		t.Error("no options should mean a hard mount")
	}
}
