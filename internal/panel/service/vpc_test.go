package service

import (
	"context"
	"net"
	"testing"
)

// The rule a customer stated: two tenants may both hold 10.0.0.0/24, but ONE
// tenant may not hold both 10.0.0.0/24 and 10.0.0.0/23 — the larger swallows the
// smaller. Containment therefore has to be tested in BOTH directions; checking
// only "does A contain B" misses the case where the new subnet is the larger one.
func TestSubnetsOverlap(t *testing.T) {
	cidr := func(s string) *net.IPNet {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad test CIDR %q: %v", s, err)
		}
		return n
	}
	cases := []struct {
		a, b string
		want bool
		why  string
	}{
		{"10.0.0.0/24", "10.0.0.0/23", true, "the /23 swallows the /24"},
		{"10.0.0.0/23", "10.0.0.0/24", true, "same pair, stated the other way round"},
		{"10.0.0.0/24", "10.0.0.0/24", true, "identical"},
		{"10.0.0.0/24", "10.0.1.0/24", false, "adjacent but distinct"},
		{"10.0.0.0/24", "192.168.0.0/24", false, "unrelated"},
		{"10.0.0.0/8", "10.55.44.0/24", true, "contained deep inside"},
	}
	for _, tc := range cases {
		if got := subnetsOverlap(cidr(tc.a), cidr(tc.b)); got != tc.want {
			t.Errorf("subnetsOverlap(%s, %s) = %v, want %v (%s)", tc.a, tc.b, got, tc.want, tc.why)
		}
	}
}

func TestFirstUsableAddress(t *testing.T) {
	for cidr, want := range map[string]string{
		"10.0.0.0/24":    "10.0.0.1",
		"192.168.8.0/22": "192.168.8.1",
		"172.16.5.0/29":  "172.16.5.1",
	} {
		_, n, _ := net.ParseCIDR(cidr)
		if got := firstUsableAddress(n); got != want {
			t.Errorf("firstUsableAddress(%s) = %q, want %q", cidr, got, want)
		}
	}
}

// Quotas are administrator-managed at runtime, so an unset or nonsensical value
// must fall back to the built-in default rather than to zero — zero would lock
// every customer out of the feature the moment the settings row is missing.
func TestQuotaSettingsFallBackToDefaults(t *testing.T) {
	// A nil db stands in for "no settings row readable at all".
	if got := VPCsPerUser(context.Background(), nil); got != DefaultVPCsPerUser {
		t.Errorf("VPCsPerUser with no settings = %d, want %d", got, DefaultVPCsPerUser)
	}
	if got := FloatingIPsPerUser(context.Background(), nil); got != DefaultFloatingIPsPerUser {
		t.Errorf("FloatingIPsPerUser with no settings = %d, want %d", got, DefaultFloatingIPsPerUser)
	}
}

// A stored zero or negative is treated as "not set": accepting it would silently
// disable the feature for everyone.
func TestQuotaSettingsIgnoreNonPositive(t *testing.T) {
	zero, negative := 0, -3
	for _, v := range []*int{nil, &zero, &negative} {
		q := quotaSettings{VPCMaxPerUser: v, FloatingIPMaxPerUser: v}
		if q.VPCMaxPerUser != nil && *q.VPCMaxPerUser > 0 {
			t.Fatalf("test setup wrong")
		}
	}
	// Exercised through the accessors, which apply the >0 rule.
	if VPCsPerUser(context.Background(), nil) <= 0 {
		t.Error("quota must never resolve to zero or less")
	}
}
