package storage

import (
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

const isoTestDomain = `<domain type='kvm'><name>vm</name><os><type>hvm</type><boot dev='hd'/><boot dev='cdrom'/></os><devices>
  <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/vm/a.qcow2'/><target dev='vda' bus='virtio'/></disk>
  <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/vm/a-data-1.qcow2'/><target dev='vdb' bus='virtio'/></disk>
  <disk type='file' device='cdrom'><source file='/vm/a-seed.iso'/><target dev='sda' bus='sata'/><readonly/></disk>
</devices></domain>`

func parseDomain(t *testing.T, s string) libvirtxml.Domain {
	t.Helper()
	var d libvirtxml.Domain
	if err := d.Unmarshal(s); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSetISODriveBootsInstallerFirstAndBack(t *testing.T) {
	withISO, err := SetISODrive(isoTestDomain, InstallISOTarget, "/cache/debian.iso")
	if err != nil {
		t.Fatal(err)
	}
	d := parseDomain(t, withISO)
	if len(d.OS.BootDevices) != 0 {
		t.Fatal("the domain-wide boot list was kept beside per-device order")
	}
	orders := map[string]uint{}
	for _, disk := range d.Devices.Disks {
		if disk.Boot != nil {
			orders[disk.Target.Dev] = disk.Boot.Order
		}
	}
	if orders["sdb"] != 1 || orders["vda"] != 2 || orders["vdb"] != 3 || len(orders) != 3 {
		t.Fatalf("boot orders %v", orders)
	}
	drive, ok, _ := FindDrive(withISO, InstallISOTarget)
	if !ok || drive.Source == nil || drive.Source.File.File != "/cache/debian.iso" || drive.Source.StartupPolicy != "optional" {
		t.Fatalf("drive %+v", drive)
	}

	// The drivers drive goes beside it and leaves the order alone.
	both, err := SetISODrive(withISO, DriversISOTarget, "/cache/virtio-win.iso")
	if err != nil {
		t.Fatal(err)
	}
	if drv, ok, _ := FindDrive(both, DriversISOTarget); !ok || drv.Boot != nil || drv.Source.File.File != "/cache/virtio-win.iso" {
		t.Fatalf("drivers drive %+v", drv)
	}

	// Ejecting keeps an empty drive, so an ISO can go in while the VM runs.
	ejected, err := SetISODrive(both, InstallISOTarget, "")
	if err != nil {
		t.Fatal(err)
	}
	d = parseDomain(t, ejected)
	if len(d.OS.BootDevices) != 2 {
		t.Fatalf("boot list not restored: %+v", d.OS.BootDevices)
	}
	drive, ok, _ = FindDrive(ejected, InstallISOTarget)
	if !ok || DrivePath(drive) != "" || drive.Boot != nil {
		t.Fatalf("ejected drive %+v", drive)
	}
	if strings.Count(ejected, `dev="sdb"`) != 1 {
		t.Fatal("the drive was duplicated")
	}
}

func TestEjectingAMissingDriveAddsNone(t *testing.T) {
	out, err := SetISODrive(isoTestDomain, DriversISOTarget, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := FindDrive(out, DriversISOTarget); ok {
		t.Fatal("an empty drive was added to a domain that had none")
	}
}

func TestSetISODriveRefusesADisk(t *testing.T) {
	if _, err := SetISODrive(isoTestDomain, "vdb", "/x.iso"); err == nil {
		t.Fatal("a disk was turned into a CD drive")
	}
	if _, err := ISOTarget(3); err == nil {
		t.Fatal("slot 3 accepted")
	}
}
