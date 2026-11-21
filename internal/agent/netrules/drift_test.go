package netrules

import (
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

// listed renders specs the way `iptables -S` lists them, near enough for the
// counting the drift check does.
func listed(chain string, specs ...[]string) []string {
	var out []string
	for _, s := range specs {
		out = append(out, "-A "+chain+" "+strings.Join(s, " "))
	}
	return out
}

func TestDrift(t *testing.T) {
	fw := VMFirewall{VMID: testVM, IPs: []string{"10.0.0.5"}, IPv6Prefix: "2001:db8:5::/64",
		Rules: []models.FirewallRule{rule("tcp", "allow", "inbound", 10)}, BlockSMTP: true}
	v4, v6, _ := BuildFirewall(fw, "b")
	chains := EbChainNames(testVM)
	healthy := HostSnapshot{
		Firewall:     listed("MABURVM-FIREWALL", append(v4.Top, v4.Bottom...)...),
		Firewall6:    listed("MABURVM-FIREWALL", append(v6.Top, v6.Bottom...)...),
		Postrouting:  []string{"-A POSTROUTING -s 10.0.0.5/32 -m comment --comment maburvm-vm-" + testVM + " -j MASQUERADE"},
		Forward:      []string{"-A FORWARD -s 10.0.0.5/32 -m comment --comment maburvm-vm-" + testVM + "-out -j ACCEPT", "-A FORWARD -d 10.0.0.5/32 -m comment --comment maburvm-vm-" + testVM + "-in -j ACCEPT"},
		PortForwards: listed("MABURVM-NAT", PortForwardRule("203.0.113.2", testVM, 2222, "10.0.0.5", 22, "tcp", "")),
		Have6:        true, HaveEbtables: true,
		EbChains: map[string]bool{chains[1]: true}, EbJumps: map[string]bool{chains[1]: true},
	}
	want := VMExpect{Firewall: fw, Primary: "10.0.0.5", Masquerade: true, PortForwards: 1, AntiSpoof: true}
	if d := Drift(want, healthy); d != nil {
		t.Fatalf("healthy host reported %q", d)
	}

	flushed := healthy
	flushed.Firewall, flushed.PortForwards, flushed.EbJumps = nil, nil, map[string]bool{}
	if d := Drift(want, flushed); len(d) != 3 {
		t.Fatalf("flushed host: %q", d)
	}

	// A stale masquerade on a public address is drift too.
	public := want
	public.Masquerade = false
	if d := Drift(public, healthy); len(d) != 1 || !strings.Contains(d[0], "masquerade") {
		t.Fatalf("stale masquerade: %q", d)
	}

	// No publish address: forwards are not checked; no ip6tables: v6 neither.
	unchecked := healthy
	unchecked.PortForwards, unchecked.Firewall6, unchecked.Have6 = nil, nil, false
	noAddr := want
	noAddr.PortForwards = -1
	if d := Drift(noAddr, unchecked); d != nil {
		t.Fatalf("unchecked parts reported %q", d)
	}
}

func TestParseEbtablesListing(t *testing.T) {
	out := "Bridge table: filter\n\nBridge chain: INPUT, entries: 0, policy: ACCEPT\n\n" +
		"Bridge chain: FORWARD, entries: 2, policy: ACCEPT\n-j MAS-0b6f3c1e5d0a4c579a55\n-j MAX-11112222333344445555\n\n" +
		"Bridge chain: OUTPUT, entries: 1, policy: ACCEPT\n-j SOMETHING\n\n" +
		"Bridge chain: MAS-0b6f3c1e5d0a4c579a55, entries: 3, policy: RETURN\n-p ARP -i vnet3 -j DROP\n"
	chains, jumps := ParseEbtablesListing(out)
	if !chains["MAS-0b6f3c1e5d0a4c579a55"] || !chains["FORWARD"] || chains["MAX-11112222333344445555"] {
		t.Fatalf("chains = %v", chains)
	}
	if !jumps["MAS-0b6f3c1e5d0a4c579a55"] || !jumps["MAX-11112222333344445555"] || jumps["SOMETHING"] {
		t.Fatalf("jumps = %v", jumps)
	}
	if n := EbChainNames(testVM); n[0] != "MAS-0b6f3c1e5d0a4c579a55" || len(n[1]) > 31 {
		t.Fatalf("names = %v", n)
	}
}

func TestPortForwardKeyMatchesListedForm(t *testing.T) {
	built := PortForwardRule("203.0.113.2", testVM, 8080, "10.0.0.5", 80, "tcp", "198.51.100.7")
	line := "-A MABURVM-NAT -s 198.51.100.7/32 -d 203.0.113.2/32 -p tcp -m tcp --dport 8080 -m comment --comment maburvm-vm-" + testVM + "-port-8080 -j DNAT --to-destination 10.0.0.5:80"
	if a, b := PortForwardKey(built), PortForwardKey(LineSpec(line)); a != b {
		t.Fatalf("built %q != listed %q", a, b)
	}
	moved := PortForwardRule("203.0.113.2", testVM, 8080, "10.0.0.6", 80, "tcp", "198.51.100.7")
	if PortForwardKey(moved) == PortForwardKey(LineSpec(line)) {
		t.Fatal("a forward to another address must not count as installed")
	}
	anySrc := PortForwardRule("203.0.113.2", testVM, 8080, "10.0.0.5", 80, "tcp", "0.0.0.0/0")
	if PortForwardKey(anySrc) != PortForwardKey(LineSpec(strings.Replace(line, "-s 198.51.100.7/32 ", "", 1))) {
		t.Fatal("any source must match a line without -s")
	}
}

func TestCountCommentAndLineAddr(t *testing.T) {
	lines := []string{
		"-A POSTROUTING -s 10.0.0.5/32 -m comment --comment maburvm-vm-" + testVM + " -j MASQUERADE",
		"-A FORWARD -s 10.0.0.5/32 -m comment --comment maburvm-vm-" + testVM + "-out -j ACCEPT",
	}
	if CountComment(lines, "maburvm-vm-"+testVM) != 1 {
		t.Fatal("the masquerade comment is a prefix of the FORWARD one and must not match it")
	}
	if LineAddr(lines[1], "-s") != "10.0.0.5" {
		t.Fatalf("addr = %q", LineAddr(lines[1], "-s"))
	}
}

func TestVPCGuestsToStart(t *testing.T) {
	bridges := map[string]bool{"mvb0b6f3c1e": true}
	got := VPCGuestsToStart([]Guest{
		{ID: "on-vpc", Autostart: true, Bridges: []string{"br0", "mvb0b6f3c1e"}},
		{ID: "running", Autostart: true, Running: true, Bridges: []string{"mvb0b6f3c1e"}},
		{ID: "kept-off", Bridges: []string{"mvb0b6f3c1e"}},
		{ID: "elsewhere", Autostart: true, Bridges: []string{"br0"}},
	}, bridges)
	if len(got) != 1 || got[0] != "on-vpc" {
		t.Fatalf("got %v", got)
	}
}
