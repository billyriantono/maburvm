package storage

import (
	"strings"
	"testing"
)

func TestIncrementalSupport(t *testing.T) {
	for _, c := range []struct {
		lib, qemu uint32
		caps      string
		ok        bool
		why       string
	}{
		{7_002_000, 6_000_000, "", true, ""},
		{8_000_000, 6_002_000, `<domainCapabilities><features><backup supported='yes'/></features></domainCapabilities>`, true, ""},
		{6_000_000, 6_000_000, "", false, "libvirt 6.0.0"},
		{7_002_000, 5_002_000, "", false, "QEMU 5.2.0"},
		{9_000_000, 8_000_000, `<domainCapabilities><features><backup supported='no'/></features></domainCapabilities>`, false, "not supported"},
	} {
		ok, why := IncrementalSupport(c.lib, c.qemu, c.caps)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%d/%d: got %v %q", c.lib, c.qemu, ok, why)
		}
	}
}

func TestChainableDisks(t *testing.T) {
	if ok, _ := ChainableDisks([]DomainDisk{{Target: "vda", Format: "qcow2"}, {Target: "vdb", Format: "qcow2"}}); !ok {
		t.Fatal("qcow2 files refused")
	}
	if ok, why := ChainableDisks([]DomainDisk{{Target: "vda", Format: "qcow2"}, {Target: "vdb", Format: "raw", Block: true}}); ok || !strings.Contains(why, "vdb") {
		t.Fatalf("a block device accepted: %q", why)
	}
}

func TestBackupAndCheckpointXML(t *testing.T) {
	disks := []DomainDisk{{Target: "vda", Format: "qcow2"}, {Target: "vdb", Format: "qcow2"}}
	x, err := BackupXML(disks, map[string]string{"vda": "/vm/a.qcow2"}, "maburvm-1")
	if err != nil {
		t.Fatal(err)
	}
	want := `<domainbackup mode="push"><incremental>maburvm-1</incremental><disks><disk name="vda" backup="yes" type="file"><target file="/vm/a.qcow2"></target><driver type="qcow2"></driver></disk><disk name="vdb" backup="no"></disk></disks></domainbackup>`
	if x != want {
		t.Fatalf("backup XML:\n%s\nwant\n%s", x, want)
	}
	full, _ := BackupXML(disks, map[string]string{"vda": "/a", "vdb": "/b"}, "")
	if strings.Contains(full, "incremental") {
		t.Fatalf("a full backup names a checkpoint: %s", full)
	}

	cx, err := CheckpointXML("maburvm-2", disks, []string{"vda", "vdb"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CheckpointTargets(`<domaincheckpoint><name>x</name><parent><name>y</name></parent><creationTime>1</creationTime>` +
		strings.TrimPrefix(strings.TrimSuffix(cx, "</domaincheckpoint>"), "<domaincheckpoint><name>maburvm-2</name>") + `</domaincheckpoint>`)
	if err != nil || !SameTargets(got, []string{"vdb", "vda"}) {
		t.Fatalf("targets %v %v from %s", got, err, cx)
	}
	if SameTargets([]string{"vda"}, []string{"vda", "vdb"}) {
		t.Fatal("different disk sets compared equal")
	}
}

func TestValidCheckpointName(t *testing.T) {
	for _, ok := range []string{"maburvm-6f1c2a3e-0000-4000-8000-000000000001", "a"} {
		if !ValidCheckpointName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a/b", "<x>", strings.Repeat("a", 101)} {
		if ValidCheckpointName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestIsBackupImageKey(t *testing.T) {
	for _, ok := range []string{"backups/vm/scheduled_1.qcow2", "prefix/backups/vm/a-vdb.qcow2"} {
		if !IsBackupImageKey(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"images/x.qcow2", "backups/../etc/x.qcow2", "/backups/x.qcow2", "backups/vm/a.raw", "backups.qcow2", "backups//x.qcow2"} {
		if IsBackupImageKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBitmapNames(t *testing.T) {
	info := `{"virtual-size": 1073741824, "format": "qcow2", "format-specific": {"type": "qcow2", "data": {"compat": "1.1",
		"bitmaps": [{"flags": ["auto"], "name": "maburvm-a", "granularity": 65536}, {"flags": [], "name": "other", "granularity": 65536}]}}}`
	got := BitmapNames([]byte(info))
	if strings.Join(got, ",") != "maburvm-a,other" {
		t.Fatalf("got %v", got)
	}
	if BitmapNames([]byte(`{"format":"raw"}`)) != nil {
		t.Fatal("a raw image has bitmaps")
	}
}

func TestMeasureRequired(t *testing.T) {
	if n, err := MeasureRequired([]byte(`{"required": 3221553152, "fully-allocated": 10739318784}`)); err != nil || n != 3221553152 {
		t.Fatalf("got %d %v", n, err)
	}
	if _, err := MeasureRequired([]byte(`qemu-img: error`)); err == nil {
		t.Fatal("garbage measured")
	}
}

func TestChainCommands(t *testing.T) {
	cmds := ChainCommands([]string{"/w/full", "/w/inc1", "/w/inc2"}, "/w/out")
	got := make([]string, len(cmds))
	for i, c := range cmds {
		got[i] = strings.Join(c, " ")
	}
	want := []string{
		"qemu-img rebase -u -f qcow2 -F qcow2 -b /w/full /w/inc1",
		"qemu-img rebase -u -f qcow2 -F qcow2 -b /w/inc1 /w/inc2",
		"qemu-img convert -f qcow2 -O qcow2 /w/inc2 /w/out",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}
