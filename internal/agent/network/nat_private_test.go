package network

import "testing"

// A VM holding a routable address must never be masqueraded: it is its own
// identity on the wire, and rewriting its source sends packets out of the VLAN
// with a source from another subnet, which the upstream router discards. The
// failure is deceptive — replies to inbound connections keep working, so the VM
// answers SSH while nothing it starts itself can reach the internet.
func TestIsPrivateAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
		why  string
	}{
		{"10.0.0.5", true, "RFC1918"},
		{"172.16.4.9", true, "RFC1918"},
		{"192.168.1.20", true, "RFC1918"},
		{"100.64.3.1", true, "carrier-grade NAT is equally unroutable, and net.IP.IsPrivate does not cover it"},
		{"169.254.1.1", true, "link-local"},
		{"127.0.0.1", true, "loopback"},

		{"103.118.174.33", false, "the address whose masquerade broke a live VM's outbound connectivity"},
		{"103.122.246.51", false, "public"},
		{"185.65.203.131", false, "public"},
		{"8.8.8.8", false, "public"},

		{"", true, "unclassifiable: masquerade, matching the previous behaviour"},
		{"not-an-ip", true, "unclassifiable"},
	}

	for _, tt := range tests {
		if got := isPrivateAddress(tt.addr); got != tt.want {
			t.Errorf("isPrivateAddress(%q) = %v, want %v — %s", tt.addr, got, tt.want, tt.why)
		}
	}
}

// A CIDR-suffixed address must classify the same way as the bare address; the
// caller is not guaranteed to strip it.
func TestIsPrivateAddressIgnoresPrefix(t *testing.T) {
	if !isPrivateAddress("10.0.0.5/24") {
		t.Error("10.0.0.5/24 is private")
	}
	if isPrivateAddress("103.118.174.33/25") {
		t.Error("103.118.174.33/25 is public and must not be masqueraded")
	}
}
