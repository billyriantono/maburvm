package netrules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckVLANBridge(t *testing.T) {
	sys := t.TempDir()
	mk := func(p ...string) {
		if err := os.MkdirAll(filepath.Join(append([]string{sys}, p...)...), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("br1", "bridge")
	mk("br1", "brif", "bond0")
	mk("bond0")
	mk("eth3")
	mk("notabridge")
	if err := os.WriteFile(filepath.Join(sys, "br1", "bridge", "vlan_filtering"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ bridge, uplink, problem string }{
		{"br1", "bond0", ""},
		{"br1", "eth3", "eth3 is not a port of br1"},
		{"br1", "eth9", "the uplink eth9 does not exist on this node"},
		{"br2", "bond0", "br2 does not exist on this node"},
		{"notabridge", "eth3", "notabridge is not a bridge"},
		{"br1;x", "bond0", "not a valid interface name"},
	} {
		if got := CheckVLANBridge(sys, c.bridge, c.uplink); got.Problem != c.problem {
			t.Errorf("%s:%s = %q, want %q", c.bridge, c.uplink, got.Problem, c.problem)
		}
	}
	if !CheckVLANBridge(sys, "br1", "bond0").Filtering {
		t.Error("filtering flag not read")
	}
}
