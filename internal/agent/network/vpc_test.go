package network

import (
	"strings"
	"testing"
)

// Interface names are capped at 15 bytes by the kernel (IFNAMSIZ). A VPC derives
// four of them from its id, so a full UUID cannot be used and the truncation has
// to stay within budget for every one of them.
func TestVPCNamesFitKernelLimit(t *testing.T) {
	id8 := vpcShortID("8e369345-e81a-4f44-9d23-15454b85b4af")
	if len(id8) != 8 {
		t.Fatalf("short id = %q, want 8 chars", id8)
	}
	for _, n := range []string{vpcBridge(id8), vpcIntVeth(id8), vpcUpVeth(id8)} {
		// The veth peers get a "p" suffix, so budget one extra byte.
		if len(n)+1 > 15 {
			t.Errorf("interface name %q (+peer suffix) exceeds IFNAMSIZ", n)
		}
	}
	// Two different VPCs must never collide on any name.
	other := vpcShortID("1ab67382-897d-460c-afbb-55c3b309299f")
	if vpcBridge(id8) == vpcBridge(other) || vpcNetns(id8) == vpcNetns(other) {
		t.Error("distinct VPCs produced identical interface/namespace names")
	}
}

// A non-hex id (or one too short) would produce interface names that collide or
// are invalid, so it must be rejected rather than silently truncated to nothing.
func TestVPCShortIDStripsNonHex(t *testing.T) {
	if got := vpcShortID("---"); got != "" {
		t.Errorf("vpcShortID(%q) = %q, want empty so the caller rejects it", "---", got)
	}
	if got := vpcShortID("ZZZZ1234abcd"); got != "1234abcd" {
		t.Errorf("vpcShortID kept non-hex characters: %q", got)
	}
}

func TestValidateVPC(t *testing.T) {
	if _, err := validateVPC("10.0.0.0/24", "10.0.0.1"); err != nil {
		t.Fatalf("valid VPC rejected: %v", err)
	}
	// Overlapping tenants are the whole point, so an identical subnet for a
	// second VPC must still validate — isolation is the namespace's job.
	if _, err := validateVPC("10.0.0.0/24", "10.0.0.1"); err != nil {
		t.Fatalf("identical subnet must remain valid: %v", err)
	}
	for _, tc := range []struct{ subnet, gw, why string }{
		{"203.0.113.0/24", "203.0.113.1", "public range would shadow real internet destinations"},
		{"169.254.128.0/24", "169.254.128.1", "link-local is reserved for host<->namespace links"},
		{"10.0.0.0/30", "10.0.0.1", "too small to host guests"},
		{"10.0.0.0/24", "10.9.9.9", "gateway outside subnet"},
		{"not-a-cidr", "10.0.0.1", "malformed"},
	} {
		if _, err := validateVPC(tc.subnet, tc.gw); err == nil {
			t.Errorf("expected rejection (%s) for %s gw %s", tc.why, tc.subnet, tc.gw)
		}
	}
}

// The host-side masquerade must cover the whole /30 link and be tagged so
// teardown can find it again.
func TestVPCHostNATRule(t *testing.T) {
	rule := strings.Join(vpcHostNATRule("169.254.128.1", "abc12345"), " ")
	if !strings.Contains(rule, "-s 169.254.128.0/30") {
		t.Fatalf("rule must match the link network, not one address: %s", rule)
	}
	if !strings.Contains(rule, "maburvm-vpc-abc12345") {
		t.Fatalf("rule must be tagged for teardown: %s", rule)
	}
}

// Regression: the /30 links are laid out back to back, so the masquerade network
// must be computed by masking. Zeroing the last octet maps .5 to …0.0/30 — the
// first VPC's link — leaving every later VPC with no masquerade and no outbound
// internet, which is exactly what happened on a live node.
func TestVPCHostNATRuleMasksNotZeroes(t *testing.T) {
	for hostIP, wantNet := range map[string]string{
		"169.254.128.1":  "169.254.128.0/30",
		"169.254.128.5":  "169.254.128.4/30",
		"169.254.128.9":  "169.254.128.8/30",
		"169.254.129.13": "169.254.129.12/30",
	} {
		rule := strings.Join(vpcHostNATRule(hostIP, "abc12345"), " ")
		if !strings.Contains(rule, "-s "+wantNet) {
			t.Errorf("host %s -> rule %q, want network %s", hostIP, rule, wantNet)
		}
	}
}

func TestPeerOfLinkAddr(t *testing.T) {
	if got := peerOfLinkAddr("169.254.128.1"); got != "169.254.128.2" {
		t.Errorf("peer of .1 = %q, want 169.254.128.2", got)
	}
}
