package storage

import (
	"encoding/xml"
	"reflect"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

const twoDiskDomain = `<domain type="kvm"><name>vm</name><devices>
  <disk type="file" device="disk"><driver name="qemu" type="qcow2"/><source file="/vm/abc.qcow2"/><target dev="vda" bus="virtio"/></disk>
  <disk type="file" device="cdrom"><driver name="qemu" type="raw"/><source file="/vm/abc-seed.iso"/><target dev="sda" bus="sata"/><readonly/></disk>
  <disk type="block" device="disk"><driver name="qemu" type="raw"/><source dev="/dev/vg0/data"/><target dev="vdb" bus="virtio"/></disk>
  <disk type="file" device="cdrom"><target dev="hdc" bus="ide"/></disk>
</devices></domain>`

func TestDomainDisksSkipsCDDrives(t *testing.T) {
	got, err := DomainDisks(twoDiskDomain)
	if err != nil {
		t.Fatal(err)
	}
	want := []DomainDisk{
		{Target: "vda", Path: "/vm/abc.qcow2", Format: "qcow2"},
		{Target: "vdb", Path: "/dev/vg0/data", Format: "raw", Block: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DomainDisks = %+v, want %+v", got, want)
	}
}

func TestBackupSnapshotMovesEveryDiskOntoAnOverlay(t *testing.T) {
	snapXML, overlays, err := BackupSnapshot(twoDiskDomain, "/vm", "abc", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 2 || overlays[0].Target != "vda" || overlays[1].Target != "vdb" {
		t.Fatalf("overlays = %+v", overlays)
	}
	if overlays[1].Base != "/dev/vg0/data" || !overlays[1].Block || overlays[1].BaseFormat != "raw" {
		t.Fatalf("block disk overlay = %+v", overlays[1])
	}
	for _, ov := range overlays {
		if !IsBackupOverlay(ov.Path) || !strings.HasPrefix(ov.Path, "/vm/abc-"+ov.Target) {
			t.Fatalf("overlay path %q is not recognisable as a backup overlay", ov.Path)
		}
	}
	var snap libvirtxml.DomainSnapshot
	if err := xml.Unmarshal([]byte(snapXML), &snap); err != nil {
		t.Fatalf("snapshot XML does not parse: %v\n%s", err, snapXML)
	}
	modes := map[string]string{}
	for _, d := range snap.Disks.Disks {
		modes[d.Name] = d.Snapshot
		if d.Snapshot == "external" && (d.Source == nil || d.Source.File == nil || !IsBackupOverlay(d.Source.File.File)) {
			t.Fatalf("external disk %s has no overlay file", d.Name)
		}
	}
	want := map[string]string{"vda": "external", "sda": "no", "vdb": "external", "hdc": "no"}
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("snapshot modes = %v, want %v", modes, want)
	}
}

func TestBackupSnapshotRefusesADiskStillOnAnOverlay(t *testing.T) {
	dom := strings.Replace(twoDiskDomain, "/vm/abc.qcow2", "/vm/abc-vda"+OverlayMarker+"7.qcow2", 1)
	if _, _, err := BackupSnapshot(dom, "/vm", "abc", 8); err == nil {
		t.Fatal("a disk left on an earlier overlay must not get a second one stacked on it")
	}
	disks, err := DomainDisks(dom)
	if err != nil {
		t.Fatal(err)
	}
	if !OnBackupOverlay(disks) {
		t.Fatal("a VM with a disk on a backup overlay must be seen as held by a backup")
	}
	if disks, _ := DomainDisks(twoDiskDomain); OnBackupOverlay(disks) {
		t.Fatal("a VM on its own disks is not held by a backup")
	}
}

// libvirt refuses an external snapshot of a read-only disk and, the snapshot
// being atomic, would fail it for every disk.
func TestBackupSnapshotLeavesReadOnlyDisksOut(t *testing.T) {
	dom := strings.Replace(twoDiskDomain, `<target dev="vdb" bus="virtio"/>`, `<target dev="vdb" bus="virtio"/><readonly/>`, 1)
	snapXML, overlays, err := BackupSnapshot(dom, "/vm", "abc", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 1 || overlays[0].Target != "vda" {
		t.Fatalf("overlays = %+v", overlays)
	}
	if !strings.Contains(snapXML, `<disk name="vdb" snapshot="no"/>`) {
		t.Fatalf("the read-only disk is not left out:\n%s", snapXML)
	}
}

func TestBackupSnapshotEscapesPaths(t *testing.T) {
	snapXML, _, err := BackupSnapshot(twoDiskDomain, `/vm/a&b"c`, "abc", 1)
	if err != nil {
		t.Fatal(err)
	}
	var snap libvirtxml.DomainSnapshot
	if err := xml.Unmarshal([]byte(snapXML), &snap); err != nil {
		t.Fatalf("snapshot XML does not parse: %v", err)
	}
	if got := snap.Disks.Disks[0].Source.File.File; !strings.HasPrefix(got, `/vm/a&b"c/`) {
		t.Fatalf("overlay path = %q", got)
	}
}

func TestRepointDiskTakesAStoppedVMOffItsOverlay(t *testing.T) {
	onOverlay := strings.Replace(twoDiskDomain, `<source dev="/dev/vg0/data"/>`, `<source file="/vm/abc-vdb`+OverlayMarker+`1.qcow2"/>`, 1)
	onOverlay = strings.Replace(onOverlay, `type="block" device="disk"><driver name="qemu" type="raw"/>`, `type="file" device="disk"><driver name="qemu" type="qcow2"/>`, 1)
	out, changed, err := RepointDisk(onOverlay, "vdb", "/dev/vg0/data", true, "raw")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	disks, err := DomainDisks(out)
	if err != nil {
		t.Fatal(err)
	}
	if disks[1] != (DomainDisk{Target: "vdb", Path: "/dev/vg0/data", Format: "raw", Block: true}) {
		t.Fatalf("vdb = %+v", disks[1])
	}
	if disks[0].Path != "/vm/abc.qcow2" {
		t.Fatalf("vda was touched: %+v", disks[0])
	}
	if _, changed, _ := RepointDisk(out, "vdz", "/x", false, "qcow2"); changed {
		t.Fatal("an unknown target must change nothing")
	}
}

func TestExportArgsHonoursCompression(t *testing.T) {
	cases := map[string][]string{
		"":     {"convert", "-U", "-O", "qcow2", "-c", "s", "d"},
		"gzip": {"convert", "-U", "-O", "qcow2", "-c", "s", "d"},
		"zstd": {"convert", "-U", "-O", "qcow2", "-c", "-o", "compression_type=zstd", "s", "d"},
		"none": {"convert", "-U", "-O", "qcow2", "s", "d"},
	}
	for c, want := range cases {
		if got := ExportArgs(c, "s", "d"); !reflect.DeepEqual(got, want) {
			t.Errorf("ExportArgs(%q) = %v, want %v", c, got, want)
		}
	}
	if !zstdUnsupported([]byte("qemu-img: Invalid parameter 'compression_type'")) {
		t.Error("a qemu-img without zstd must be recognised")
	}
}

func TestBackupKeysAndPaths(t *testing.T) {
	if got := ExtraDiskKey("backups/v/v_1.qcow2", "vdb"); got != "backups/v/v_1-vdb.qcow2" {
		t.Errorf("ExtraDiskKey = %q", got)
	}
	if got := ExtraDiskKey("backups/v/v_1.tar.gz", "vdc"); got != "backups/v/v_1.tar.gz-vdc" {
		t.Errorf("ExtraDiskKey = %q", got)
	}
	if got := RestoredDiskPath("/vm", "id", "vdb"); got != "/vm/id-data-vdb.qcow2" {
		t.Errorf("RestoredDiskPath = %q", got)
	}
	have := []DomainDisk{{Target: "vda"}, {Target: "vdc"}}
	if got := MissingTargets([]string{"vda", "vdb", "vdc"}, have); !reflect.DeepEqual(got, []string{"vdb"}) {
		t.Errorf("MissingTargets = %v", got)
	}
}

func TestRestoreMethodMatchesTheDisk(t *testing.T) {
	cases := map[DomainDisk]string{
		{Format: "qcow2"}:              RestoreRename,
		{Format: "raw"}:                RestoreConvertFile,
		{Format: "raw", Block: true}:   RestoreWriteDevice,
		{Format: "qcow2", Block: true}: RestoreWriteDevice,
	}
	for d, want := range cases {
		if got := RestoreMethod(d); got != want {
			t.Errorf("RestoreMethod(%+v) = %s, want %s", d, got, want)
		}
	}
}
