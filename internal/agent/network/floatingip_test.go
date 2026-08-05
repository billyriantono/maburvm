package network

import (
	"strings"
	"testing"
)

// The floating IP rules must live in chains distinct from the ones carrying the
// baseline per-VM MASQUERADE and the port forwards. That separation is what makes
// detach safe: it can flush everything belonging to an address without stripping
// the VM's outbound NAT (which would leave it with no internet and no obvious
// cause) or shadowing an existing port forward.
func TestFloatingIPChainsAreSeparate(t *testing.T) {
	for _, c := range []string{MaburVMChain, PostroutingChain, PreroutingChain} {
		if FloatingIPChain == c || FloatingIPPostChain == c {
			t.Fatalf("floating IP chain collides with %s", c)
		}
	}
	if FloatingIPChain == FloatingIPPostChain {
		t.Fatal("ingress and egress floating IP chains must differ")
	}
}

func TestFloatingIPRules(t *testing.T) {
	dnat := strings.Join(floatingDNATRule("203.0.113.10", "10.20.0.5"), " ")
	if !strings.Contains(dnat, "-d 203.0.113.10/32") || !strings.Contains(dnat, "--to-destination 10.20.0.5") {
		t.Fatalf("DNAT rule does not 1:1 map the floating IP to the VM: %s", dnat)
	}
	if !strings.Contains(dnat, "-j DNAT") {
		t.Fatalf("DNAT rule missing target: %s", dnat)
	}

	snat := strings.Join(floatingSNATRule("203.0.113.10", "10.20.0.5"), " ")
	if !strings.Contains(snat, "-s 10.20.0.5/32") || !strings.Contains(snat, "--to-source 203.0.113.10") {
		t.Fatalf("SNAT rule does not make the VM egress as the floating IP: %s", snat)
	}

	// Both rules must be keyed by the address alone, so detach can find them
	// without knowing which VM the address currently points at.
	want := floatingComment("203.0.113.10")
	if !strings.Contains(dnat, want) || !strings.Contains(snat, want) {
		t.Fatalf("rules not keyed by floating IP comment %q", want)
	}
}

func TestFloatingRuleMatchesIsExact(t *testing.T) {
	comment := floatingComment("10.0.0.4")
	line := `-A MABURVM-FIP -d 10.0.0.4/32 -m comment --comment "` + comment + `" -j DNAT --to-destination 192.168.1.2`
	if !floatingRuleMatches(line, comment) {
		t.Fatal("own rule should match")
	}

	// A different address whose comment merely starts with this one must NOT
	// match, or detaching 10.0.0.4 would also tear down 10.0.0.42.
	other := floatingComment("10.0.0.42")
	otherLine := `-A MABURVM-FIP -d 10.0.0.42/32 -m comment --comment "` + other + `" -j DNAT --to-destination 192.168.1.3`
	if floatingRuleMatches(otherLine, comment) {
		t.Fatal("prefix-colliding address must not match")
	}

	// A VM's baseline masquerade carries a different comment scheme entirely and
	// must never be swept up by a floating IP detach.
	masq := `-A POSTROUTING -s 192.168.1.2/32 -m comment --comment "maburvm-vm-abc" -j MASQUERADE`
	if floatingRuleMatches(masq, comment) {
		t.Fatal("baseline masquerade must never match a floating IP detach")
	}
}

func TestValidateFloatingArgs(t *testing.T) {
	if err := validateFloatingArgs("203.0.113.10", "10.20.0.5"); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	for _, tc := range []struct{ fip, internal string }{
		{"not-an-ip", "10.20.0.5"},
		{"203.0.113.10", ""},
		{"203.0.113.10", "203.0.113.10"}, // NATing an address to itself is a loop
	} {
		if err := validateFloatingArgs(tc.fip, tc.internal); err == nil {
			t.Fatalf("expected rejection for (%q, %q)", tc.fip, tc.internal)
		}
	}
}

// A directly-bridged VM (its own public IP, upstream gateway as next hop) replies
// without transiting the host, so conntrack never undoes the DNAT and the client
// RSTs the mismatched SYN-ACK. Such a VM needs the hairpin. A private-addressed
// VM is behind the host, so its replies already come back through and the real
// client IP survives — adding a hairpin there would hide it for nothing.
func TestNeedsHairpin(t *testing.T) {
	for ip, want := range map[string]bool{
		"203.0.113.163": true,  // directly bridged public
		"198.51.100.7":  true,  // directly bridged public
		"10.20.0.5":     false, // behind the host
		"192.168.1.20":  false,
		"172.16.4.9":    false,
		"127.0.0.1":     false,
		"169.254.1.1":   false,
		"":              false,
	} {
		if got := needsHairpin(ip); got != want {
			t.Errorf("needsHairpin(%q) = %v, want %v", ip, got, want)
		}
	}
}

// The hairpin must key on the connection's ORIGINAL destination. Matching only
// on -d would also rewrite the source of port-forward connections to the same
// VM, which arrive on the node's own address and have nothing to do with the
// floating IP.
func TestHairpinRuleIsScopedToTheFloatingIP(t *testing.T) {
	rule := strings.Join(floatingHairpinRule("203.0.113.10", "10.20.0.5"), " ")
	if !strings.Contains(rule, "--ctorigdst 203.0.113.10") {
		t.Fatalf("hairpin must match the original destination, else it catches port forwards too: %s", rule)
	}
	if !strings.Contains(rule, "--to-source 203.0.113.10") {
		t.Fatalf("hairpin must SNAT to the floating IP so the reply returns via the host: %s", rule)
	}
	// Shares the address-keyed comment, so detach flushes it with everything else.
	if !strings.Contains(rule, floatingComment("203.0.113.10")) {
		t.Fatalf("hairpin rule must carry the floating IP comment so detach removes it: %s", rule)
	}
}

// libvirt guards its own NAT networks with "ACCEPT established, REJECT the rest",
// so a floating IP into a private network DNATs correctly and is then rejected in
// FORWARD — the address answers ARP and the counters climb while nothing ever
// connects. The FORWARD accept must therefore live in its own chain, jumped from
// the head of FORWARD, and be scoped to the connections that arrived on this
// floating IP rather than opening the VM up generally.
func TestFloatingForwardRuleIsScopedAndSeparate(t *testing.T) {
	if FloatingIPFwdChain == FloatingIPChain || FloatingIPFwdChain == FloatingIPPostChain {
		t.Fatal("FORWARD chain must be distinct from the nat-table chains")
	}
	rule := strings.Join(floatingForwardRule("203.0.113.10", "10.99.0.10"), " ")
	if !strings.Contains(rule, "--ctorigdst 203.0.113.10") {
		t.Fatalf("must only accept traffic that arrived on this floating IP: %s", rule)
	}
	if !strings.Contains(rule, "-j ACCEPT") || !strings.Contains(rule, "-d 10.99.0.10/32") {
		t.Fatalf("must accept traffic destined to the VM: %s", rule)
	}
	// Shares the address-keyed comment so detach sweeps it up with the rest.
	if !strings.Contains(rule, floatingComment("203.0.113.10")) {
		t.Fatalf("FORWARD rule must carry the floating IP comment: %s", rule)
	}
}
