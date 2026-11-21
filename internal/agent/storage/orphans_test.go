package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testVM = "0b6a3f4e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"

func TestClassifyDiskFile(t *testing.T) {
	cases := []struct {
		name, reason, vm string
		mine, ok         bool
	}{
		{testVM + ".qcow2", "VM system disk", testVM, true, true},
		{testVM + "-data-1700000000.qcow2", "VM data disk", testVM, true, true},
		{testVM + "-data-vdb.qcow2", "VM data disk", testVM, true, true},
		{testVM + "-seed.iso", "VM setup disc (cloud-init seed)", testVM, true, true},
		{testVM + ".qcow2.backup.1700000000", "copy kept by a reinstall or restore that did not finish", testVM, true, true},
		{testVM + ".qcow2.restorebak.1700000000", "copy kept by a reinstall or restore that did not finish", testVM, true, true},
		{testVM + "-vda-backup.qcow2", "backup export left behind", testVM, true, true},
		{testVM + "-vda" + OverlayMarker + "1700000000.qcow2", "backup overlay left behind", testVM, true, true},
		{testVM + ".qcow2.move-abc123.part", "unfinished disk move", testVM, true, true},
		{"clone-" + testVM + ".qcow2", "copy pulled for a clone from another node", testVM, true, true},
		{"old-server.img", "disk image not made by MaburVM", "", false, true},
		{"debian.qcow2.part", "unfinished download or copy", "", false, true},
		{"notes.txt", "", "", false, false},
		{"domain.xml", "", "", false, false},
	}
	for _, c := range cases {
		reason, vm, mine, ok := ClassifyDiskFile(c.name)
		if reason != c.reason || vm != c.vm || mine != c.mine || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v, %v), want (%q, %q, %v, %v)", c.name, reason, vm, mine, ok, c.reason, c.vm, c.mine, c.ok)
		}
	}
}

func TestFindOrphanFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	used := write(testVM+".qcow2", 48*time.Hour)
	orphan := write("11111111-2222-4333-8444-555555555555.qcow2", 48*time.Hour)
	write("22222222-2222-4333-8444-555555555555.qcow2", time.Minute) // a create at work
	write("readme.txt", 48*time.Hour)
	if err := os.Mkdir(filepath.Join(dir, "sub.qcow2"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := FindOrphanFiles([]string{dir, dir, "relative"}, map[string]bool{used: true}, OrphanMinAge, now)
	if len(got) != 1 || got[0].Path != orphan || got[0].Kind != "file" || !got[0].MaburVM || got[0].Pool != dir {
		t.Fatalf("got %+v", got)
	}
}

func TestOrphanLVs(t *testing.T) {
	pools := []LVMPool{{VG: "vg0"}, {VG: "vg1", Thin: "thin"}}
	lvs := []LV{
		{VG: "vg0", Name: "maburvm-" + testVM + "-vda", Tags: []string{LVMTag}, Attr: "-wi-a-----", Size: 10},
		{VG: "vg0", Name: "maburvm-" + testVM + "-vdb", Tags: []string{LVMTag}, Attr: "-wi-ao----"}, // open
		{VG: "vg0", Name: "used", Attr: "-wi-a-----"},
		{VG: "vg0", Name: "thinpool", Attr: "twi-aotz--"}, // a thin pool is never a disk
		{VG: "vg1", Name: "imported", Pool: "thin", Attr: "Vwi-a-tz--"},
		{VG: "vg1", Name: "other", Pool: "elsewhere", Attr: "Vwi-a-tz--"}, // in no registered pool
		{VG: "vg9", Name: "x", Attr: "-wi-a-----"},
		{VG: "vg0", Name: "maburvm-vol-scratch", Tags: []string{LVMTag}, Attr: "-wi-a-----"},
	}
	got := OrphanLVs(lvs, pools, map[string]bool{"/dev/vg0/used": true})
	want := []struct {
		path, vm string
		mine     bool
	}{
		{"/dev/vg0/maburvm-" + testVM + "-vda", testVM, true},
		{"/dev/vg0/maburvm-vol-scratch", "", true},
		{"/dev/vg1/imported", "", false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].VMID != w.vm || got[i].MaburVM != w.mine || got[i].Kind != "lv" {
			t.Errorf("%d: got %+v, want %+v", i, got[i], w)
		}
	}
}

func TestTemplateCacheName(t *testing.T) {
	url := "https://cloud.example/images/debian-12.qcow2?x=1"
	plain := TemplateCacheName(url, "")
	pinned := TemplateCacheName(url, "ABCDEF0123456789"+"00000000000000000000000000000000000000000000000")
	if !IsCacheDownload(plain) || !IsCacheDownload(pinned) {
		t.Fatalf("names must stay evictable downloads: %s %s", plain, pinned)
	}
	if plain == pinned || filepath.Ext(plain) != ".qcow2" || pinned[13:29] != "abcdef0123456789" {
		t.Fatalf("plain %s pinned %s", plain, pinned)
	}
	if got := TemplateCacheName("https://x/win.qcow2.zip", ""); filepath.Ext(got) != ".qcow2" {
		t.Fatalf("zip suffix kept: %s", got)
	}

	dir := t.TempDir()
	for _, n := range []string{plain, pinned, pinned + ".part", TemplateCacheName("https://other/x.qcow2", "")} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := StaleTemplateCopies(dir, url, pinned)
	if len(stale) != 1 || stale[0] != filepath.Join(dir, plain) {
		t.Fatalf("stale %v", stale)
	}
	if IsCacheDownload(UploadCacheName(pinned[13:29]+"0000000000000000000000000000000000000000000000000", "virtio.iso")) {
		t.Fatal("an uploaded image must never be evicted")
	}
}

func TestVerifySHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(p, "2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824"); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(p, "00"); err == nil {
		t.Fatal("a wrong checksum passed")
	}
}

func TestParseBackingChain(t *testing.T) {
	out := []byte(`[{"filename":"/vm/a.qcow2","full-backing-filename":"/vm/base.qcow2","format":"qcow2"},
		{"filename":"/vm/base.qcow2","format":"qcow2"}]`)
	got, err := ParseBackingChain(out)
	if err != nil || len(got) != 3 || got[0] != "/vm/a.qcow2" || got[1] != "/vm/base.qcow2" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseBackingChain([]byte("nope")); err == nil {
		t.Fatal("garbage parsed")
	}
}
