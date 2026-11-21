package netrules

import (
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

// The panel accepts "80", "1000-11000" and "80,443"; iptables wants a colon for
// a range and -m multiport for anything that is not a single port. Rules are
// built as a set, so one unparsable entry must fail the build rather than slip
// through as a rule iptables refuses.
func TestNormalizePortSpec(t *testing.T) {
	tests := []struct {
		in        string
		want      string
		wantMulti bool
		wantErr   bool
	}{
		{in: "80", want: "80"},
		{in: "1000-11000", want: "1000:11000", wantMulti: true},
		{in: "1000:11000", want: "1000:11000", wantMulti: true},
		{in: "80,443,8443", want: "80,443,8443", wantMulti: true},
		{in: "80,1000-2000", want: "80,1000:2000", wantMulti: true},
		{in: "1-65535", want: "1:65535", wantMulti: true},
		{in: "0", wantErr: true},
		{in: "65536", wantErr: true},
		{in: "11000-1000", wantErr: true},
		{in: "http", wantErr: true},
		{in: "80:443:8443", wantErr: true},
		{in: "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16", wantErr: true},
	}
	for _, tt := range tests {
		got, multi, err := normalizePortSpec(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("normalizePortSpec(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want || multi != tt.wantMulti {
			t.Errorf("normalizePortSpec(%q) = %q, %v, %v; want %q, %v", tt.in, got, multi, err, tt.want, tt.wantMulti)
		}
	}
}

func rule(proto, action, dir string, prio int) models.FirewallRule {
	return models.FirewallRule{ID: "r", Protocol: proto, Action: action, Direction: dir, Priority: prio}
}

func joined(specs [][]string) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = strings.Join(s, " ")
	}
	return out
}

func TestBuildFirewallRemoteEnd(t *testing.T) {
	in := rule("tcp", "allow", "inbound", 10)
	in.PortRange, in.SourceIP = "22", "198.51.100.0/24"
	out := rule("tcp", "allow", "outbound", 10)
	out.PortRange, out.SourceIP = "22", "198.51.100.0/24"
	v6src := rule("tcp", "allow", "inbound", 10)
	v6src.SourceIP = "2001:db8::/32"

	v4, v6, err := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, IPv6Prefix: "2001:db8:5::/64", Rules: []models.FirewallRule{in, out, v6src}}, "a")
	if err != nil {
		t.Fatal(err)
	}
	top := joined(v4.Top)
	if len(top) != 2 || !strings.HasPrefix(top[0], "-p tcp -s 198.51.100.0/24 -d 192.0.2.10 --dport 22") ||
		!strings.HasPrefix(top[1], "-p tcp -s 192.0.2.10 -d 198.51.100.0/24 --dport 22") {
		t.Fatalf("v4 top = %q", top)
	}
	// The v4-sourced rules cannot match IPv6; the v6-sourced one only can.
	if got := joined(v6.Top); len(got) != 1 || !strings.HasPrefix(got[0], "-p tcp -s 2001:db8::/32 -d 2001:db8:5::/64") {
		t.Fatalf("v6 top = %q", got)
	}
}

func TestBuildFirewallDefaultDropAndPolicy(t *testing.T) {
	fw := VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10", "192.0.2.11"}, Rules: []models.FirewallRule{rule("tcp", "allow", "inbound", 10)}}
	v4, _, err := BuildFirewall(fw, "b")
	if err != nil {
		t.Fatal(err)
	}
	// Each address gets the rule and its own default drop, below the conntrack
	// accept rather than at the top.
	if len(v4.Top) != 2 || len(v4.Bottom) != 2 {
		t.Fatalf("top %d bottom %d, want 2 and 2", len(v4.Top), len(v4.Bottom))
	}
	want := "-d 192.0.2.11 -m comment --comment maburvm-vm-" + testVM + "-b-default-drop -j DROP"
	if got := strings.Join(v4.Bottom[1], " "); got != want {
		t.Fatalf("default drop = %q\nwant %q", got, want)
	}

	fw.DefaultAccept = true
	if v4, _, _ := BuildFirewall(fw, "a"); len(v4.Bottom) != 0 {
		t.Fatalf("accept policy still drops: %q", joined(v4.Bottom))
	}
	// No rules: nothing at all, which is what a legacy VM has today.
	if v4, v6, _ := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, IPv6Prefix: "2001:db8::/64"}, "a"); v4.Len()+v6.Len() != 0 {
		t.Fatalf("a VM without rules got %d entries", v4.Len()+v6.Len())
	}
}

func TestBuildFirewallRejectICMPLogFamily(t *testing.T) {
	rej := rule("tcp", "reject", "inbound", 5)
	rej.PortRange = "3306"
	ping := rule("icmp", "allow", "inbound", 10)
	ping.ICMPType = "echo-request"
	nd := rule("icmp", "allow", "inbound", 11)
	nd.ICMPType, nd.Family = "neighbour-solicitation", models.FirewallFamilyV6
	logged := rule("udp", "deny", "inbound", 20)
	logged.Log, logged.Family = true, models.FirewallFamilyV4
	udpReject := rule("udp", "reject", "inbound", 30)

	v4, v6, err := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, IPv6Prefix: "2001:db8:5::/64",
		Rules: []models.FirewallRule{logged, ping, rej, nd, udpReject}}, "a")
	if err != nil {
		t.Fatal(err)
	}
	top4 := joined(v4.Top)
	if len(top4) != 5 {
		t.Fatalf("v4 top = %q", top4)
	}
	if !strings.HasSuffix(top4[0], "-j REJECT --reject-with tcp-reset") {
		t.Errorf("tcp reject = %q", top4[0])
	}
	if !strings.Contains(top4[1], "-p icmp -d 192.0.2.10 --icmp-type echo-request") {
		t.Errorf("echo-request = %q", top4[1])
	}
	if !strings.Contains(top4[2], "-m limit") || v4.Top[2][len(v4.Top[2])-1] != LogPrefix(testVM, "deny") || !strings.HasSuffix(top4[3], "-j DROP") {
		t.Errorf("logged deny = %q / %q", top4[2], top4[3])
	}
	if !strings.HasSuffix(top4[4], "-j REJECT") {
		t.Errorf("udp reject = %q", top4[4])
	}

	top6 := joined(v6.Top)
	// v4-only logged rule is left out; ND exists in IPv6 only.
	if len(top6) != 4 || !strings.Contains(top6[1], "-p icmpv6 -d 2001:db8:5::/64 --icmpv6-type echo-request") ||
		!strings.Contains(top6[2], "--icmpv6-type neighbour-solicitation") {
		t.Fatalf("v6 top = %q", top6)
	}
	if p := LogPrefix(testVM, "reject"); len(p) > 29 {
		t.Fatalf("log prefix %q is longer than iptables allows", p)
	}
}

func TestBuildFirewallSMTPBlockComesFirst(t *testing.T) {
	allow25 := rule("tcp", "allow", "outbound", 1)
	allow25.PortRange = "25"
	v4, v6, err := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10", "192.0.2.11"}, IPv6Prefix: "2001:db8:5::/64",
		Rules: []models.FirewallRule{allow25}, BlockSMTP: true}, "a")
	if err != nil {
		t.Fatal(err)
	}
	top := joined(v4.Top)
	want := "-s 192.0.2.10 -p tcp --dport 25 -m comment --comment maburvm-vm-" + testVM + "-a-smtp -j REJECT --reject-with tcp-reset"
	if top[0] != want || !strings.HasPrefix(top[1], "-s 192.0.2.11 -p tcp --dport 25") || !strings.Contains(top[2], "ACCEPT") {
		t.Fatalf("v4 top = %q", top)
	}
	if got := joined(v6.Top); !strings.HasPrefix(got[0], "-s 2001:db8:5::/64 -p tcp --dport 25") {
		t.Fatalf("v6 top = %q", got)
	}
	// Mail blocking alone needs no rules and leaves the rest of the traffic alone.
	v4, _, _ = BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, BlockSMTP: true}, "a")
	if len(v4.Top) != 1 || len(v4.Bottom) != 0 {
		t.Fatalf("block alone: %q / %q", joined(v4.Top), joined(v4.Bottom))
	}
}

func TestBuildFirewallRefusesTheWholeSetOnOneBadRule(t *testing.T) {
	bad := rule("tcp", "allow", "inbound", 10)
	bad.PortRange = "11000-1000"
	numeric := rule("icmp", "allow", "inbound", 10)
	numeric.ICMPType = "8" // a number without a family is ambiguous
	for _, r := range []models.FirewallRule{bad, numeric, rule("gre", "allow", "inbound", 10), rule("tcp", "maybe", "inbound", 10)} {
		if _, _, err := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, Rules: []models.FirewallRule{rule("tcp", "allow", "inbound", 1), r}}, "a"); err == nil {
			t.Errorf("rule %+v was accepted", r)
		}
	}
	numeric.Family = models.FirewallFamilyV4
	if _, _, err := BuildFirewall(VMFirewall{VMID: testVM, IPs: []string{"192.0.2.10"}, Rules: []models.FirewallRule{numeric}}, "a"); err != nil {
		t.Fatalf("numeric type with a family: %v", err)
	}
}

func TestNextGenerationAndLineSpec(t *testing.T) {
	legacy := "-A MABURVM-FIREWALL -d 192.0.2.10/32 -p tcp -m tcp --dport 22 -j ACCEPT -m comment --comment maburvm-vm-" + testVM + "-rule-"
	if g := NextGeneration([]string{legacy}, testVM); g != "a" {
		t.Fatalf("after legacy entries: %q", g)
	}
	genA := "-A MABURVM-FIREWALL -d 192.0.2.10/32 -m comment --comment maburvm-vm-" + testVM + "-a-default-drop -j DROP"
	if g := NextGeneration([]string{legacy, genA}, testVM); g != "b" {
		t.Fatalf("after generation a: %q", g)
	}
	// Another VM's entries say nothing about this one.
	if g := NextGeneration([]string{strings.ReplaceAll(genA, testVM, "11111111-2222-3333-4444-555555555555")}, testVM); g != "a" {
		t.Fatalf("other VM: %q", g)
	}

	logLine := `-A MABURVM-FIREWALL -d 192.0.2.10/32 -p udp -m limit --limit 10/min --limit-burst 20 -m comment --comment maburvm-vm-x-a-log-r -j LOG --log-prefix "MBVM 0b6f3c1e DENY "`
	spec := LineSpec(logLine)
	if spec[len(spec)-1] != "MBVM 0b6f3c1e DENY " || spec[0] != "-d" {
		t.Fatalf("spec = %q", spec)
	}
	if LineSpec("-N MABURVM-FIREWALL") != nil {
		t.Fatal("a chain line is not a rule")
	}
}
