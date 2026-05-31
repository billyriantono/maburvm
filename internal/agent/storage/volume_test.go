package storage

import (
	"reflect"
	"testing"
)

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

func TestLVCreateArgs(t *testing.T) {
	got := lvCreateArgs("vg0", "data1", 10)
	want := []string{"-L", "10G", "-n", "data1", "vg0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lvCreateArgs = %v, want %v", got, want)
	}
	if p := lvDevicePath("vg0", "data1"); p != "/dev/vg0/data1" {
		t.Fatalf("lvDevicePath = %q", p)
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
	// lvm: must be a /dev device path.
	if err := m.DeleteVolume("lvm", "relative/path"); err == nil {
		t.Error("expected guard error for non-device lvm path")
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
