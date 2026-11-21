package models

import "testing"

func TestSplitLVMPoolPath(t *testing.T) {
	cases := []struct {
		typ, path, vg, thin string
		ok                  bool
	}{
		{PoolTypeLVM, "vg0", "vg0", "", true},
		{PoolTypeLVM, " vg0 ", "vg0", "", true},
		{PoolTypeLVMThin, "vg0/thin", "vg0", "thin", true},
		{PoolTypeLVMThin, "vg0", "", "", false},
		{PoolTypeLVMThin, "vg0/", "", "", false},
		{PoolTypeLVM, "/dev/vg0", "", "", false},
		{PoolTypeLVM, "-vg", "", "", false},
		{PoolTypeLVM, "..", "", "", false},
		{PoolTypeDir, "/data", "", "", false},
	}
	for _, c := range cases {
		vg, thin, err := SplitLVMPoolPath(c.typ, c.path)
		if (err == nil) != c.ok || vg != c.vg || thin != c.thin {
			t.Errorf("%s %q: %q %q %v", c.typ, c.path, vg, thin, err)
		}
	}
}

func TestLVMNames(t *testing.T) {
	const vm = "11111111-2222-3333-4444-555555555555"
	if name := LVMDiskName(vm, "vda"); name != "maburvm-"+vm+"-vda" || !OwnsLVMName(vm, name) {
		t.Fatalf("name %q not owned", name)
	}
	for _, other := range []string{"maburvm-" + vm, "maburvm-" + vm + "-", "maburvm-" + vm + "-VDA", "maburvm-" + vm + "-vda-x", "legacy101-0", "maburvm-99999999-2222-3333-4444-555555555555-vda"} {
		if OwnsLVMName(vm, other) {
			t.Errorf("%q owned", other)
		}
	}
	if OwnsLVMName("", "maburvm--vda") {
		t.Error("an empty VM owns nothing")
	}
	if !LVMPoolHolds(PoolTypeLVMThin, "vg0/thin", "/dev/vg0/x") || LVMPoolHolds(PoolTypeLVM, "vg0", "/dev/vg01/x") || LVMPoolHolds(PoolTypeLVM, "vg0", "/dev/vg0/../sda") {
		t.Error("LVMPoolHolds")
	}
	if !MaburVMVolumePath("/dev/vg0/maburvm-x-vda") || MaburVMVolumePath("/dev/vg0/legacy101") || MaburVMVolumePath("/vm/maburvm-x.qcow2") {
		t.Error("MaburVMVolumePath")
	}
}
