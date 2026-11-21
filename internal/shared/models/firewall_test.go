package models

import "testing"

func TestFirewallRuleCheckCombination(t *testing.T) {
	for _, c := range []struct {
		name string
		rule FirewallRule
		ok   bool
	}{
		{"plain rule", FirewallRule{Protocol: "tcp"}, true},
		{"named type in both families", FirewallRule{Protocol: "icmp", ICMPType: "echo-request"}, true},
		{"type on a tcp rule", FirewallRule{Protocol: "tcp", ICMPType: "echo-request"}, false},
		{"unknown name", FirewallRule{Protocol: "icmp", ICMPType: "ping"}, false},
		{"number without a family", FirewallRule{Protocol: "icmp", ICMPType: "8"}, false},
		{"number with a family", FirewallRule{Protocol: "icmp", ICMPType: "8/0", Family: FirewallFamilyV4}, true},
		{"v6-only name without a family", FirewallRule{Protocol: "icmp", ICMPType: "packet-too-big"}, false},
		{"v6-only name on v4", FirewallRule{Protocol: "icmp", ICMPType: "packet-too-big", Family: FirewallFamilyV4}, false},
		{"v6-only name on v6", FirewallRule{Protocol: "icmp", ICMPType: "packet-too-big", Family: FirewallFamilyV6}, true},
		{"out of range", FirewallRule{Protocol: "icmp", ICMPType: "300", Family: FirewallFamilyV4}, false},
		{"v6 source on a v4 rule", FirewallRule{Protocol: "tcp", SourceIP: "2001:db8::/32", Family: FirewallFamilyV4}, false},
		{"anywhere fits either family", FirewallRule{Protocol: "tcp", SourceIP: "0.0.0.0/0", Family: FirewallFamilyV6}, true},
	} {
		if err := c.rule.CheckCombination(); (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}
