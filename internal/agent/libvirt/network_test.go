package libvirt

import (
	"strings"
	"testing"
)

func TestGatewayFromCIDR(t *testing.T) {
	gw, prefix, err := gatewayFromCIDR("10.20.0.0/24")
	if err != nil || gw != "10.20.0.1" || prefix != 24 {
		t.Fatalf("got (%q, %d, %v), want (10.20.0.1, 24, nil)", gw, prefix, err)
	}
	if _, _, err := gatewayFromCIDR("not-a-cidr"); err == nil {
		t.Error("expected error for invalid CIDR")
	}
}

func TestGenerateBridgeName(t *testing.T) {
	b := generateBridgeName("maburvm-some-network-id")
	if !strings.HasPrefix(b, "mvbr") {
		t.Errorf("bridge name %q should start with mvbr", b)
	}
	if len(b) > 15 {
		t.Errorf("bridge name %q exceeds the 15-char Linux interface limit", b)
	}
	// Deterministic for the same input.
	if b != generateBridgeName("maburvm-some-network-id") {
		t.Error("bridge name should be stable for the same input")
	}
	if b == generateBridgeName("different") {
		t.Error("different inputs should yield different bridge names")
	}
}

func TestDHCPRange(t *testing.T) {
	start, end := dhcpRange("10.20.0.0/24")
	if start != "10.20.0.2" || end != "10.20.0.254" {
		t.Errorf("got (%q, %q), want (10.20.0.2, 10.20.0.254)", start, end)
	}
	// IPv6 yields no v4 range.
	if s, e := dhcpRange("2001:db8::/64"); s != "" || e != "" {
		t.Errorf("IPv6 should yield empty range, got (%q, %q)", s, e)
	}
}
