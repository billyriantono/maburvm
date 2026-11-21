package storage

import (
	"regexp"
	"strings"
	"testing"
)

var betweenTags = regexp.MustCompile(`>\s+<`)

func compact(s string) string { return betweenTags.ReplaceAllString(s, "><") }

const snapDomain = `<domain type="kvm"><name>vm1</name><os><type>hvm</type><boot dev="hd"></boot><boot dev="cdrom"></boot></os><devices>
<disk type="file" device="disk"><driver name="qemu" type="qcow2"></driver><source file="/vm/a.qcow2"></source><target dev="vda" bus="virtio"></target></disk>
<disk type="file" device="disk"><driver name="qemu" type="qcow2"></driver><source file="/vm/b.qcow2"></source><target dev="vdb" bus="virtio"></target></disk>
<disk type="file" device="cdrom"><driver name="qemu" type="raw"></driver><source file="/vm/a-seed.iso"></source><target dev="sda" bus="sata"></target><readonly></readonly></disk>
</devices></domain>`

func TestQueryBlockNodesMatchesByFile(t *testing.T) {
	reply := `{"return":[{"device":"","qdev":"/machine/peripheral/virtio-disk1/virtio-backend","inserted":{"file":"/vm/b.qcow2","node-name":"libvirt-2-format"}},
		{"device":"drive-virtio-disk0","inserted":{"file":"/vm/a.qcow2"}},
		{"device":"","qdev":"sata0-0-0","inserted":{"file":"/vm/a-seed.iso","node-name":"libvirt-3-format"}}]}`
	disks := []DomainDisk{{Target: "vda", Path: "/vm/a.qcow2", Format: "qcow2"}, {Target: "vdb", Path: "/vm/b.qcow2", Format: "qcow2"}}
	nodes, err := QueryBlockNodes(reply, disks)
	if err != nil || nodes["vda"] != "drive-virtio-disk0" || nodes["vdb"] != "libvirt-2-format" {
		t.Fatalf("nodes %v %v", nodes, err)
	}
	if _, err := QueryBlockNodes(reply, append(disks, DomainDisk{Target: "vdc", Path: "/vm/c.qcow2"})); err == nil {
		t.Fatal("a disk QEMU does not list was accepted")
	}
	cmd := InternalSnapshotCommand("s1", []string{"libvirt-2-format", "drive-virtio-disk0"})
	if !strings.Contains(cmd, `"execute":"transaction"`) || strings.Count(cmd, "blockdev-snapshot-internal-sync") != 2 || !strings.Contains(cmd, `"name":"s1"`) {
		t.Fatal(cmd)
	}
}

func TestDiskOnlySnapshotXML(t *testing.T) {
	x, err := DiskOnlySnapshotXML("nightly <1>", 1700000000, snapDomain)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<name>nightly &lt;1&gt;</name>", "<state>shutoff</state>", "<memory snapshot='no'/>",
		`<disk name="vda" snapshot="internal"/>`, `<disk name="vdb" snapshot="internal"/>`, `<disk name="sda" snapshot="no"/>`,
		"<creationTime>1700000000</creationTime>", "<name>vm1</name>"} {
		if !strings.Contains(x, want) {
			t.Fatalf("missing %s in %s", want, x)
		}
	}
	if !IsDiskOnlySnapshot(x) || IsDiskOnlySnapshot("<domainsnapshot><description>Snapshot</description></domainsnapshot>") {
		t.Fatal("marker not recognised")
	}
}

func TestRescueDiskBootsFirstAndComesOutCleanly(t *testing.T) {
	in, err := SetRescueDisk(snapDomain, "/vm/vm1-rescue.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	in = compact(in)
	if p, _ := RescueDiskPath(in); p != "/vm/vm1-rescue.qcow2" {
		t.Fatalf("rescue disk %q", p)
	}
	if strings.Contains(in, `<boot dev=`) || !strings.Contains(in, `<target dev="vdz" bus="virtio"></target><boot order="1"></boot>`) ||
		!strings.Contains(in, `<target dev="vda" bus="virtio"></target><boot order="2"></boot>`) {
		t.Fatal(in)
	}
	// Doing it again replaces, never stacks.
	again, _ := SetRescueDisk(in, "/vm/vm1-rescue.qcow2")
	if strings.Count(again, `dev="vdz"`) != 1 {
		t.Fatal(again)
	}
	out, err := SetRescueDisk(again, "")
	if err != nil {
		t.Fatal(err)
	}
	out = compact(out)
	if strings.Contains(out, "vdz") || strings.Contains(out, "boot order") || !strings.Contains(out, `<boot dev="hd"></boot>`) {
		t.Fatal(out)
	}
	if p, _ := RescueDiskPath(out); p != "" {
		t.Fatal("rescue disk still found")
	}
}
