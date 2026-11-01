package network

import "testing"

func TestParseAntiSpoofRule(t *testing.T) {
	const id = "0c788d51-cbb4-4757-8fdd-001bc5241adb"
	for line, want := range map[string][2]string{
		"-A MABURVM-ANTISPOOF ! -s 203.0.113.18/32 -m physdev --physdev-in vnet17 --physdev-is-bridged -m comment --comment maburvm-antispoof-" + id + "-out -j DROP":    {id, "203.0.113.18/32"},
		"-A MABURVM-ANTISPOOF ! -d 2001:db8:1::/64 -m physdev --physdev-out vnet3 --physdev-is-bridged -m comment --comment \"maburvm-antispoof-" + id + "-in6\" -j DROP": {id, "2001:db8:1::/64"},
		"-A MABURVM-ANTISPOOF -j RETURN": {"", ""},
	} {
		if vmID, addr := parseAntiSpoofRule(line); vmID != want[0] || addr != want[1] {
			t.Errorf("parseAntiSpoofRule(%q) = %q, %q; want %q, %q", line, vmID, addr, want[0], want[1])
		}
	}
	if got := ebChain(id); len(got) > 31 || got != "MAS-0c788d51cbb447578fdd" {
		t.Errorf("ebChain = %q", got)
	}
}
