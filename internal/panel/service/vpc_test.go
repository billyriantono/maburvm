package service

import (
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
