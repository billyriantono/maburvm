package network

import (
	"reflect"
	"strings"
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
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %v\nwant %v", got, want)
	}
	del := ipv6RouteDeleteCommands(d, ll)
	if len(del) != 2 || del[0][3] != "del" || del[1][3] != "del" {
		t.Errorf("delete commands = %v", del)
	}
	if _, err := (IPv6Delegation{Prefix: "2001:db8:20:5::/64", MAC: d.MAC}).validate(); err == nil {
		t.Error("missing bridge accepted")
	}
}

func TestIPv6FirewallSpecs(t *testing.T) {
	rules := []FirewallRule{
		{ID: "ssh", Protocol: "tcp", PortRange: "22", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 10},
		{ID: "ping", Protocol: "icmp", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 20},
		{ID: "office", Protocol: "tcp", PortRange: "3306", Action: "allow", Direction: "inbound", SourceIP: "203.0.113.0/24", Priority: 30},
		{ID: "v6net", Protocol: "udp", PortRange: "5000-5100", Action: "deny", Direction: "inbound", SourceIP: "2001:db8::/32", Priority: 40},
		{ID: "egress", Protocol: "all", Action: "deny", Direction: "outbound", SourceIP: "", Priority: 50},
	}
	specs, drop := ipv6FirewallSpecs("vm1", "2001:db8:20:5::/64", rules)
	want := [][]string{
		{"-p", "tcp", "-d", "2001:db8:20:5::/64", "--dport", "22", "-j", "ACCEPT", "-m", "comment", "--comment", "maburvm-vm-vm1-rule-ssh"},
		{"-p", "icmpv6", "-d", "2001:db8:20:5::/64", "-j", "ACCEPT", "-m", "comment", "--comment", "maburvm-vm-vm1-rule-ping"},
		// "office" is IPv4-scoped and must be skipped
		{"-p", "udp", "-s", "2001:db8::/32", "-d", "2001:db8:20:5::/64", "-m", "multiport", "--dports", "5000:5100", "-j", "DROP", "-m", "comment", "--comment", "maburvm-vm-vm1-rule-v6net"},
		{"-s", "2001:db8:20:5::/64", "-j", "DROP", "-m", "comment", "--comment", "maburvm-vm-vm1-rule-egress"},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("specs =\n%v\nwant\n%v", specs, want)
	}
	if strings.Join(drop, " ") != "-d 2001:db8:20:5::/64 -j DROP -m comment --comment maburvm-vm-vm1-default-drop" {
		t.Errorf("default drop = %v", drop)
	}
}
