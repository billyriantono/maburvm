package network

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEUI64LinkLocal(t *testing.T) {
	for mac, want := range map[string]string{
		"52:54:00:12:34:56": "fe80::5054:ff:fe12:3456",
		"52:54:00:AB:CD:EF": "fe80::5054:ff:feab:cdef",
		"00:00:00:00:00:00": "fe80::200:ff:fe00:0",
	} {
		got, err := EUI64LinkLocal(mac)
		if err != nil || got != want {
			t.Errorf("EUI64LinkLocal(%q) = %q, %v; want %q", mac, got, err, want)
		}
	}
	if _, err := EUI64LinkLocal("not-a-mac"); err == nil {
		t.Error("garbage MAC accepted")
	}
}

func TestIPv6RouteCommands(t *testing.T) {
	d := IPv6Delegation{Prefix: "2001:db8:20:5::/64", MAC: "52:54:00:12:34:56", Bridge: "viifbr0"}
	ll, err := d.validate()
	if err != nil {
		t.Fatal(err)
	}
	got := ipv6RouteCommands(d, ll)
	want := [][]string{
		{"ip", "-6", "neigh", "replace", "fe80::5054:ff:fe12:3456", "lladdr", "52:54:00:12:34:56", "dev", "viifbr0", "nud", "permanent"},
		{"ip", "-6", "route", "replace", "2001:db8:20:5::/64", "via", "fe80::5054:ff:fe12:3456", "dev", "viifbr0"},
		{"ip", "-6", "neigh", "replace", "proxy", "2001:db8:20:5::1", "dev", "viifbr0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v\nwant %v", got, want)
	}
	del := ipv6RouteDeleteCommands(d, ll)
	if len(del) != 3 || del[0][3] != "del" || del[1][3] != "del" || del[2][3] != "del" {
		t.Errorf("delete commands = %v", del)
	}
	if _, err := (IPv6Delegation{Prefix: "2001:db8:20:5::/64", MAC: d.MAC}).validate(); err == nil {
		t.Error("missing bridge accepted")
	}
}

// Further addresses get their own proxy entries (a node whose upstream treats
// the pool as on-link answers for them), and one outside the prefix none.
func TestIPv6ExtraAddressesAreProxied(t *testing.T) {
	d := IPv6Delegation{Prefix: "2001:db8:20:5::/64", MAC: "52:54:00:12:34:56", Bridge: "viifbr0",
		Extra: []string{"2001:db8:20:5::25", "2001:db8:99::1", "2001:db8:20:5::1"}}
	ll, _ := d.validate()
	got := ipv6RouteCommands(d, ll)
	if len(got) != 4 || got[3][5] != "2001:db8:20:5::25" {
		t.Fatalf("commands = %v", got)
	}
	if del := ipv6RouteDeleteCommands(d, ll); len(del) != 4 || del[1][5] != "2001:db8:20:5::25" {
		t.Fatalf("delete = %v", del)
	}
	if d.Same(IPv6Delegation{Prefix: d.Prefix, MAC: d.MAC, Bridge: d.Bridge}) {
		t.Fatal("a changed list of addresses must be re-applied")
	}
	// A prefix that does not parse (a hand-edited state file) must not panic.
	if got := (IPv6Delegation{Prefix: "bogus", Extra: d.Extra}).proxied(); len(got) != 1 {
		t.Fatalf("proxied with a bad prefix = %v", got)
	}
}

func TestIPv6DelegationsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ipv6-delegations.json")
	if got := loadIPv6Delegations(path); len(got) != 0 {
		t.Fatalf("missing file: %v", got)
	}
	want := map[string]IPv6Delegation{"vm-1": {Prefix: "2001:db8:20:1::/64", MAC: "52:54:00:12:34:56", Bridge: "viifbr0"}}
	if err := saveIPv6Delegations(path, want); err != nil {
		t.Fatal(err)
	}
	if got := loadIPv6Delegations(path); len(got) != 1 || !got["vm-1"].Same(want["vm-1"]) {
		t.Fatalf("round trip: %v", got)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadIPv6Delegations(path); len(got) != 0 {
		t.Fatalf("unreadable file must load empty: %v", got)
	}
}
