package migration

import (
	"strings"
	"testing"
)

const rbdDomain = `<domain type='kvm'><name>x</name><devices>
<disk type='network' device='disk'><driver name='qemu' type='raw' cache='none'/>
  <source protocol='rbd' name='vms/maburvm-x-vda'><host name='10.0.0.1'/><auth username='maburvm'><secret type='ceph' uuid='11111111-2222-3333-4444-555555555555'/></auth></source>
  <target dev='vda' bus='virtio'/></disk>
<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='/var/lib/libvirt/images/x-seed.iso'/><target dev='sda' bus='sata'/><readonly/></disk>
</devices></domain>`

// An image on RBD is named by its disk path, and a move leaves it where it
// is: the same image on the destination, never another place.
func TestRBDDriveStaysWhereItIs(t *testing.T) {
	const disk = "rbd://11111111-2222-3333-4444-555555555555/vms/maburvm-x-vda"
	drives, err := Drives(rbdDomain)
	if err != nil || len(drives) != 2 {
		t.Fatalf("Drives = %+v, %v", drives, err)
	}
	if d := drives[0]; d.Path != disk || d.SourceType != "network" || !d.Copied() || d.Cache != "none" {
		t.Errorf("the image reads as %+v", d)
	}
	if !drives[1].Seed {
		t.Errorf("the setup disc reads as %+v", drives[1])
	}
	out, err := RewriteDisks(rbdDomain, map[string]Dest{"vda": {Path: disk}, "sda": {Path: "/data/x-seed.iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `name="vms/maburvm-x-vda"`) || !strings.Contains(out, `uuid="11111111-2222-3333-4444-555555555555"`) || !strings.Contains(out, "/data/x-seed.iso") {
		t.Errorf("rewritten as:\n%s", out)
	}
	if _, err := RewriteDisks(rbdDomain, map[string]Dest{"vda": {Path: "/data/x.qcow2"}}); err == nil || !strings.Contains(err.Error(), "Ceph RBD") {
		t.Errorf("an image sent to a file: %v", err)
	}
}

// The panel maps a drive that stays on RBD to its own disk path. The agent
// took only absolute paths, so every move of a VM on RBD was refused.
func TestRBDDestinationIsAccepted(t *testing.T) {
	const disk = "rbd://11111111-2222-3333-4444-555555555555/vms/maburvm-x-vda"
	for _, c := range []struct {
		path   string
		copied bool
		want   bool
	}{
		{disk, false, true},
		{disk, true, false}, // nothing is copied into RBD
		{"rbd://not-a-cluster/vms/x", false, false},
		{"/data/x.qcow2", true, true},
		{"/data/../x.qcow2", true, false},
		{"data/x.qcow2", false, false},
	} {
		if got := DestOK(c.path, c.copied); got != c.want {
			t.Errorf("DestOK(%q, %v) = %v, want %v", c.path, c.copied, got, c.want)
		}
	}
}
