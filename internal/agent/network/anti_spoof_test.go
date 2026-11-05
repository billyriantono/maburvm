package network

import (
	"fmt"
	"strings"
	"testing"
)

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

func TestAssignAddrs(t *testing.T) {
	nics := []NIC{
		{MAC: "52:54:00:00:00:01", Tap: "vnet1", Bridge: "br0"},
		{MAC: "52:54:00:00:00:02", Tap: "vnet2", Bridge: "br106"},
		{MAC: "52:54:00:00:00:03", Tap: "vnet3", Bridge: "br500"}, // no panel address: unrestricted
	}
	got := assignAddrs([]SpoofAddr{
		{IP: "198.51.100.54", Bridge: "br0"},
		{IP: "203.0.113.33", Bridge: "br106"},
		{IP: "100.64.0.9"}, // no pool bridge: first NIC
	}, nics)
	if len(got) != 2 || got[0].Tap != "vnet1" || got[1].Tap != "vnet2" {
		t.Fatalf("assignAddrs = %+v", got)
	}
	if fmt.Sprint(got[0].IPs) != "[198.51.100.54 100.64.0.9]" || fmt.Sprint(got[1].IPs) != "[203.0.113.33]" {
		t.Errorf("assignAddrs IPs = %v / %v", got[0].IPs, got[1].IPs)
	}
	stopped := []NIC{{MAC: "52:54:00:00:00:01"}}
	if got := assignAddrs([]SpoofAddr{{IP: "198.51.100.54"}}, stopped); len(got) != 0 {
		t.Errorf("stopped VM got rules: %+v", got)
	}
}

func TestEbRuleCommandsRoundTrip(t *testing.T) {
	const id = "0c788d51-cbb4-4757-8fdd-001bc5241adb"
	cmds := ebRuleCommands(id, []spoofNIC{
		{NIC: NIC{MAC: "52:54:00:00:00:01", Tap: "vnet1"}, IPs: []string{"198.51.100.54", "100.64.0.9"}},
		{NIC: NIC{MAC: "52:54:00:00:00:02", Tap: "vnet2"}, IPs: []string{"203.0.113.33"}},
	})
	var allowed []string
	ipv4 := map[string]bool{}
	for _, c := range cmds {
		// ebtables -L prints a rule as its -A arguments without "-A <chain>".
		if c[0] == "-A" && c[1] == ebChain(id) {
			rule := strings.Join(c[2:], " ")
			if tap, ip := parseChainRule(rule); ip != "" {
				allowed = append(allowed, tap+"/"+ip)
			}
			ipv4[rule] = true
		}
	}
	if fmt.Sprint(allowed) != "[vnet1/198.51.100.54 vnet1/100.64.0.9 vnet2/203.0.113.33]" {
		t.Errorf("addresses read back from the chain = %v", allowed)
	}
	// A multi-homed guest replies from one address out of another NIC.
	if !ipv4["-p IPv4 -i vnet1 --ip-src 203.0.113.33 -j RETURN"] || !ipv4["-p IPv4 -o vnet2 --ip-dst 198.51.100.54 -j RETURN"] {
		t.Error("IPv4 is not allowed for the VM's other addresses on each NIC")
	}
	if last := cmds[len(cmds)-1]; strings.Join(last, " ") != "-A FORWARD -j "+ebChain(id) {
		t.Errorf("last command = %v, want the FORWARD jump", last)
	}
}
