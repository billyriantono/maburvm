package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDataDiskDir(t *testing.T) {
	pool, vmDir, fallback := t.TempDir(), t.TempDir(), t.TempDir()
	primary := filepath.Join(vmDir, "vm.qcow2")
	file := filepath.Join(pool, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, poolPath, primaryDisk, want string
	}{
		{"the pool the panel named", pool, primary, pool},
		{"no pool named: beside the VM's own disk", "", primary, vmDir},
		{"a missing pool is not created", filepath.Join(pool, "typo"), primary, vmDir},
		{"a file is not a pool", file, primary, vmDir},
		{"a relative path is not a pool", "data", primary, vmDir},
		{"nothing known: the node default", "", "", fallback},
		{"the VM's disk directory is gone", "", "/nonexistent-dir/vm.qcow2", fallback},
		{"a VM on a block device: never a file in /dev", "", "/dev/null", fallback},
	}
	for _, c := range cases {
		if got := DataDiskDir(c.poolPath, c.primaryDisk, fallback); got != c.want {
			t.Errorf("%s: DataDiskDir(%q, %q) = %q, want %q", c.name, c.poolPath, c.primaryDisk, got, c.want)
		}
	}
}

func TestNormalizePoolType(t *testing.T) {
	cases := map[string]string{
		"":                  "dir",
		"dir":               "dir",
		"file":              "dir",
		"Directory":         "dir",
		"lvm":               "lvm",
		"lvmthin":           "lvm",
		"zfs":               "zfs",
		"zfsthin":           "zfs",
		"zfscompressed":     "zfs",
		"zfsthincompressed": "zfs",
		"ceph":              "ceph", // unsupported -> passes through, errors at create
	}
	for in, want := range cases {
		if got := normalizePoolType(in); got != want {
			t.Errorf("normalizePoolType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestZFSCreateArgs(t *testing.T) {
	got := zfsCreateArgs("tank", "data1", 20)
	want := []string{"create", "-V", "20G", "tank/data1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zfsCreateArgs = %v, want %v", got, want)
	}
	if p := zfsDevicePath("tank", "data1"); p != "/dev/zvol/tank/data1" {
		t.Fatalf("zfsDevicePath = %q", p)
	}
}

func TestVolumeManagerCreateValidation(t *testing.T) {
	m := NewVolumeManager()
	if _, _, err := m.CreateVolume("dir", "/pool", "v", "qcow2", 0); err == nil {
		t.Error("expected error for non-positive size")
	}
	if _, _, err := m.CreateVolume("ceph", "/pool", "v", "", 10); err == nil {
		t.Error("expected unsupported-pool-type error for ceph")
	}
}

func TestVolumeManagerDeleteGuards(t *testing.T) {
	m := NewVolumeManager()
	// dir: only volume-extension files may be removed.
	if err := m.DeleteVolume("dir", "/etc/passwd"); err == nil {
		t.Error("expected guard error deleting a non-volume file")
	}
	// lvm: never here; LVM proves a volume is a registered pool's first.
	if err := m.DeleteVolume("lvm", "/dev/sda"); err == nil {
		t.Error("expected lvm delete to be refused")
	}
	if _, _, err := m.CreateVolume("lvmthin", "vg0", "v", "", 10); err == nil {
		t.Error("expected lvm create to be refused")
	}
	// zfs: must be under /dev/zvol/.
	if err := m.DeleteVolume("zfs", "/dev/sda"); err == nil {
		t.Error("expected guard error for non-zvol path")
	}
	// unsupported type.
	if err := m.DeleteVolume("ceph", "/dev/rbd0"); err == nil {
		t.Error("expected unsupported error for ceph delete")
	}
}
