package storage

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const vmA = "11111111-2222-3333-4444-555555555555"

func TestParseLVs(t *testing.T) {
	out := []byte("  vg0|maburvm-" + vmA + "-vda|thin|maburvm|21474836480|Vwi-aotz--\n" +
		"  vg0|imported||a,b|1073741824|-wi-a-----\n\n")
	lvs, err := parseLVs(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []LV{
		{VG: "vg0", Name: "maburvm-" + vmA + "-vda", Pool: "thin", Tags: []string{"maburvm"}, Size: 21474836480, Attr: "Vwi-aotz--"},
		{VG: "vg0", Name: "imported", Tags: []string{"a", "b"}, Size: 1073741824, Attr: "-wi-a-----"},
	}
	if !reflect.DeepEqual(lvs, want) {
		t.Fatalf("got %+v", lvs)
	}
	if !lvs[0].Open() || lvs[1].Open() || !lvs[0].Tagged() || lvs[1].Tagged() {
		t.Fatal("open/tagged misread")
	}
	if _, err := parseLVs([]byte("vg0|x|1\n")); err == nil {
		t.Fatal("a short row must not parse")
	}
}

// lvs answers a volume group's path with every volume in it; a group holding
// just one must not pass for that volume.
func TestLookupIsTheDeviceItself(t *testing.T) {
	row := []byte("vg0|data-disk||a|1073741824|-wi-a-----\n")
	l := &LVM{run: &fakeRunner{handler: func(string, []string) CommandResult { return CommandResult{Stdout: row} }}}
	for _, dev := range []string{"/dev/vg0/data-disk", "/dev/mapper/vg0-data--disk", "/dev/vg0//data-disk"} {
		if lv, err := l.Lookup(t.Context(), dev); err != nil || lv.Name != "data-disk" {
			t.Errorf("%s: %+v %v", dev, lv, err)
		}
	}
	for _, dev := range []string{"/dev/vg0", "/dev/vg0/other", "/dev/mapper/vg0-data-disk"} {
		if _, err := l.Lookup(t.Context(), dev); err == nil {
			t.Errorf("%s passed for /dev/vg0/data-disk", dev)
		}
	}
}

func TestHeldByAndRemovable(t *testing.T) {
	pools := []LVMPool{{VG: "vg0", Thin: "thin"}, {VG: "vg1"}}
	owned := LV{VG: "vg0", Name: "maburvm-" + vmA + "-vda", Pool: "thin", Tags: []string{LVMTag}, Attr: "Vwi-a-tz--"}
	thickInGroup := LV{VG: "vg1", Name: "maburvm-" + vmA + "-d1", Tags: []string{LVMTag}, Attr: "-wi-a-----"}
	foreign := LV{VG: "vg1", Name: "legacy101-disk0", Attr: "-wi-a-----"}
	outside := LV{VG: "ubuntu-vg", Name: "maburvm-" + vmA + "-vda", Tags: []string{LVMTag}, Attr: "-wi-a-----"}
	otherThin := LV{VG: "vg0", Name: "x", Pool: "thin2", Attr: "Vwi-a-tz--"}

	if p, ok := HeldBy(owned, pools); !ok || p.Thin != "thin" {
		t.Fatalf("owned held by %v %v", p, ok)
	}
	if _, ok := HeldBy(otherThin, pools); ok {
		t.Fatal("a volume in an unregistered thin pool is not held by a thin registration of another")
	}
	if _, ok := HeldBy(LV{VG: "vg1", Pool: "t9"}, pools); !ok {
		t.Fatal("a thick registration covers its whole volume group")
	}

	cases := []struct {
		name    string
		lv      LV
		vm      string
		foreign bool
		ok      bool
	}{
		{"owned", owned, vmA, false, true},
		{"owned thick", thickInGroup, vmA, false, true},
		{"another VM's", owned, "99999999-2222-3333-4444-555555555555", false, false},
		{"foreign refused", foreign, vmA, false, false},
		{"foreign confirmed", foreign, vmA, true, true},
		{"outside every pool", outside, vmA, true, false},
		{"open", LV{VG: "vg1", Name: "maburvm-" + vmA + "-vda", Tags: []string{LVMTag}, Attr: "-wi-ao----"}, vmA, true, false},
		{"untagged name", LV{VG: "vg1", Name: "maburvm-" + vmA + "-vda", Attr: "-wi-a-----"}, vmA, false, false},
		{"thin pool itself", LV{VG: "vg1", Name: "thin", Attr: "twi-aotz--"}, vmA, true, false},
		{"snapshot", LV{VG: "vg1", Name: "snap", Attr: "swi-a-s---"}, vmA, true, false},
	}
	for _, c := range cases {
		err := CheckRemovable(c.lv, c.vm, pools, c.foreign)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestCreateArgs(t *testing.T) {
	f := &fakeRunner{}
	l := &LVM{run: f}
	if _, err := l.Create(t.Context(), LVMPool{VG: "vg0"}, "maburvm-x-vda", 10<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Create(t.Context(), LVMPool{VG: "vg0", Thin: "thin"}, "maburvm-x-vdb", 5<<30); err != nil {
		t.Fatal(err)
	}
	want := []runnerCall{
		{"lvcreate", []string{"--yes", "--name", "maburvm-x-vda", "--addtag", LVMTag, "--size", "10737418240b", "--wipesignatures", "y", "vg0"}},
		{"lvcreate", []string{"--yes", "--name", "maburvm-x-vdb", "--addtag", LVMTag, "--type", "thin", "--virtualsize", "5368709120b", "--thinpool", "thin", "vg0"}},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %+v", f.calls)
	}
	for _, bad := range []string{"", "-rf", "a/b", ".."} {
		if _, err := l.Create(t.Context(), LVMPool{VG: "vg0"}, bad, 1); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestCapacityParsing(t *testing.T) {
	c, err := parseVGCapacity([]byte("  1000204886016|400204886016\n"))
	if err != nil || c.Total != 1000204886016 || c.Available != 400204886016 || c.Used != 600000000000 {
		t.Fatalf("vg = %+v %v", c, err)
	}
	tp := LVMPool{VG: "vg0", Thin: "thin"}
	c, err = parseThinCapacity(tp, []byte("  107374182400|25.00|3.50|twi-aotz--\n"))
	if err != nil || c.Used != 26843545600 || c.Available != 80530636800 || c.MetadataPercent != 3.5 {
		t.Fatalf("thin = %+v %v", c, err)
	}
	if _, err := parseThinCapacity(tp, []byte("107374182400|||-wi-a-----")); err == nil {
		t.Fatal("a plain volume named as a thin pool must be refused")
	}
}

func TestHostDevices(t *testing.T) {
	mounts := []byte("/dev/mapper/ubuntu--vg-root / ext4 rw 0 0\n/dev/sda1 /boot ext4 rw 0 0\n" +
		"/dev/mapper/data-vm /vm xfs rw 0 0\ntmpfs /run tmpfs rw 0 0\n")
	swaps := []byte("Filename Type Size Used Priority\n/dev/dm-1 partition 100 0 -2\n")
	got := hostDevices(mounts, swaps)
	want := []string{"/dev/mapper/ubuntu--vg-root", "/dev/sda1", "/dev/dm-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// The root volume group is refused whole; a thin pool in it is not, unless
// the host's system lives in that thin pool.
func TestCheckHost(t *testing.T) {
	rows := map[string]string{
		"/dev/mapper/pve-root": "pve|root||| 1|-wi-ao----",
		"/dev/mapper/pve-swap": "pve|swap||| 1|-wi-ao----",
		"/dev/mapper/os-root":  "os|root|thin|| 1|Vwi-aotz--",
	}
	l := &LVM{run: &fakeRunner{handler: func(cmd string, args []string) CommandResult {
		if r, ok := rows[args[len(args)-1]]; ok {
			return CommandResult{Stdout: []byte(r + "\n")}
		}
		return CommandResult{Err: errors.New("not an LV")}
	}}}
	devs := []string{"/dev/sda1", "/dev/mapper/pve-root", "/dev/mapper/pve-swap", "/dev/mapper/os-root"}
	cases := []struct {
		pool LVMPool
		ok   bool
	}{
		{LVMPool{VG: "pve"}, false},
		{LVMPool{VG: "pve", Thin: "data"}, true},
		{LVMPool{VG: "os", Thin: "thin"}, false},
		{LVMPool{VG: "vms"}, true},
	}
	for _, c := range cases {
		if err := l.checkHost(t.Context(), c.pool, devs); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.pool, err)
		}
	}
}

func TestZeroRange(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "dev")
	if err := os.WriteFile(dev, []byte(strings.Repeat("x", 3<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	l := &LVM{run: f}
	// From an unaligned offset: the head up to 1 MiB is written, the rest discarded.
	if err := l.ZeroRange(t.Context(), dev, 1000, 3<<20); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dev)
	if b[999] != 'x' || b[1000] != 0 || b[(1<<20)-1] != 0 || b[1<<20] != 'x' {
		t.Fatal("head not zeroed exactly")
	}
	want := []runnerCall{{"blkdiscard", []string{"--zeroout", "--offset", "1048576", "--length", "2097152", dev}}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %+v", f.calls)
	}
	f.calls = nil
	if err := l.ZeroRange(t.Context(), dev, 3<<20, 3<<20); err != nil || len(f.calls) != 0 {
		t.Fatal("an empty range does nothing")
	}
}

func TestProvisionThin(t *testing.T) {
	pool := LVMPool{VG: "vg0", Thin: "thin"}
	name := "maburvm-" + vmA + "-vda"
	f := &fakeRunner{handler: func(cmd string, args []string) CommandResult {
		switch {
		case cmd == "qemu-img" && args[0] == "info":
			return CommandResult{Stdout: []byte(`{"virtual-size": 32212254720, "format": "qcow2"}`)}
		case cmd == "qemu-img" && args[0] == "--help":
			return CommandResult{Stdout: []byte("convert [--target-is-zero] ...")}
		}
		return CommandResult{}
	}}
	l := &LVM{run: f}
	dev, err := l.Provision(t.Context(), pool, name, 10<<30, "/tpl/ubuntu.qcow2", "")
	if err != nil || dev != "/dev/vg0/"+name {
		t.Fatalf("dev %q err %v", dev, err)
	}
	var create, convert []string
	for _, c := range f.calls {
		switch {
		case c.name == "lvcreate":
			create = c.args
		case c.name == "qemu-img" && c.args[0] == "convert":
			convert = c.args
		}
	}
	// Never smaller than the image.
	if !hasArg(create, "32212254720b") {
		t.Errorf("lvcreate %v", create)
	}
	if want := []string{"convert", "-n", "-O", "raw", "--target-is-zero", "/tpl/ubuntu.qcow2", "/dev/vg0/" + name}; !reflect.DeepEqual(convert, want) {
		t.Errorf("convert %v", convert)
	}
}

func TestProvisionFailureRemovesVolume(t *testing.T) {
	pool := LVMPool{VG: "vg0", Thin: "thin"}
	name := "maburvm-" + vmA + "-vdb"
	created := false
	f := &fakeRunner{handler: func(cmd string, args []string) CommandResult {
		switch {
		case cmd == "lvcreate":
			created = true
		case cmd == "lvs" && created:
			return CommandResult{Stdout: []byte("vg0|" + name + "|thin|maburvm|1073741824|Vwi-a-tz--\n")}
		case cmd == "qemu-img" && args[0] == "info":
			return CommandResult{Stdout: []byte(`{"virtual-size": 1073741824}`)}
		case cmd == "qemu-img" && args[0] == "convert":
			return CommandResult{Err: errors.New("exit status 1")}
		}
		return CommandResult{}
	}}
	l := &LVM{run: f}
	if _, err := l.Provision(t.Context(), pool, name, 1<<30, "/backup.qcow2", "qcow2"); err == nil {
		t.Fatal("a failed write must fail the provision")
	}
	last := f.calls[len(f.calls)-1]
	if last.name != "lvremove" || last.args[len(last.args)-1] != "vg0/"+name {
		t.Fatalf("the new volume was not removed: %+v", f.calls)
	}
}

func TestProvisionRefusesExisting(t *testing.T) {
	name := "maburvm-" + vmA + "-vda"
	f := &fakeRunner{handler: func(cmd string, args []string) CommandResult {
		if cmd == "lvs" {
			return CommandResult{Stdout: []byte("vg0|" + name + "||maburvm|1073741824|-wi-a-----\n")}
		}
		return CommandResult{}
	}}
	l := &LVM{run: f}
	if _, err := l.Provision(t.Context(), LVMPool{VG: "vg0"}, name, 1<<30, "", ""); err == nil {
		t.Fatal("an existing volume must not be written over")
	}
	for _, c := range f.calls {
		if c.name == "lvcreate" || c.name == "lvremove" {
			t.Fatalf("unexpected %s", c.name)
		}
	}
}
