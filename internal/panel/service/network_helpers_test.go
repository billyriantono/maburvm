package service

import "testing"

func TestHostOnlyIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.10":    "203.0.113.10",
		"203.0.113.10/24": "203.0.113.10",
		"203.0.113.10/32": "203.0.113.10",
		"2001:db8::1":     "2001:db8::1",
		"2001:db8::1/64":  "2001:db8::1",
		"":                "",
	}
	for in, want := range cases {
		if got := hostOnlyIP(in); got != want {
			t.Errorf("hostOnlyIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetmaskFromCIDR(t *testing.T) {
	cases := map[string]string{
		"203.0.113.0/24": "255.255.255.0",
		"10.0.0.0/8":     "255.0.0.0",
		"192.168.1.0/30": "255.255.255.252",
		"2001:db8::/64":  "/64",
		"":               "",
		"not-a-cidr":     "",
	}
	for in, want := range cases {
		if got := netmaskFromCIDR(in); got != want {
			t.Errorf("netmaskFromCIDR(%q) = %q, want %q", in, got, want)
		}
	}
}
