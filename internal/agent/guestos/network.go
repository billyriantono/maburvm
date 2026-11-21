package guestos

import (
	"fmt"
	"net"
	"strings"
)

// GuestAddr is an IPv4 address a guest holds besides its primary one. It goes
// on the same card, after the primary, so the primary stays the source of the
// guest's own connections.
type GuestAddr struct {
	Address string // IPv4, no prefix
	Prefix  int
}

// ValidateExtra checks the extra addresses of a network config.
func ValidateExtra(extra []GuestAddr) error {
	for _, a := range extra {
		if ip := net.ParseIP(a.Address); ip == nil || ip.To4() == nil || a.Prefix < 1 || a.Prefix > 32 {
			return fmt.Errorf("invalid extra address %q/%d", a.Address, a.Prefix)
		}
	}
	return nil
}

// LinuxNetwork is the addressing a systemd-networkd guest is given.
type LinuxNetwork struct {
	MAC     string
	Address string // IPv4, no prefix
	Prefix  int
	Gateway string
	Extra   []GuestAddr
	// IPv6Address is in CIDR form; empty means no IPv6.
	IPv6Address string
	IPv6Gateway string
	IPv6DNS     []string
}

// NetworkdUnit is the systemd-networkd unit that gives the guest its static
// addresses; it matches the NIC by MAC so interface naming never matters.
//
// With IPv6 the guest also gets its address and gateway. RA is refused (there
// is none to accept, and a rogue one must not win) and link-local generation
// is pinned to EUI-64: the host's route points at the MAC-derived fe80::
// address, so a guest that picked a stable-privacy link-local would be
// unreachable. Older networkd logs an unknown-key warning for that line and
// carries on, which is fine since it already defaults to EUI-64.
func NetworkdUnit(n LinuxNetwork) string {
	var b strings.Builder
	b.WriteString("[Match]\n")
	fmt.Fprintf(&b, "MACAddress=%s\n\n", strings.ToLower(n.MAC))
	if n.IPv6Address != "" {
		b.WriteString("[Link]\nIPv6LinkLocalAddressGenerationMode=eui64\n\n")
	}
	b.WriteString("[Network]\n")
	fmt.Fprintf(&b, "Address=%s/%d\n", n.Address, n.Prefix)
	if n.Gateway != "" {
		fmt.Fprintf(&b, "Gateway=%s\n", n.Gateway)
	}
	for _, a := range n.Extra {
		fmt.Fprintf(&b, "Address=%s/%d\n", a.Address, a.Prefix)
	}
	if n.IPv6Address != "" {
		fmt.Fprintf(&b, "Address=%s\nGateway=%s\nIPv6AcceptRA=no\n", n.IPv6Address, n.IPv6Gateway)
	}
	b.WriteString("DNS=1.1.1.1\nDNS=8.8.8.8\n")
	for _, d := range n.IPv6DNS {
		fmt.Fprintf(&b, "DNS=%s\n", d)
	}
	return b.String()
}

// AnnounceUnit pings the gateway and an external anchor for a minute after
// boot. The gateway ping teaches the local L2; the external ping traverses the
// full upstream path so every hop holding a stale ARP/route for a reused IP
// relearns it promptly.
func AnnounceUnit(gateway string) string {
	return "[Unit]\n" +
		"Description=MaburVM: announce IP to gateway/upstream (populate ARP)\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		fmt.Sprintf("ExecStart=/bin/sh -c 'for i in $(seq 1 30); do ping -c1 -w2 %s >/dev/null 2>&1; ping -c1 -w2 1.1.1.1 >/dev/null 2>&1; sleep 2; done; exit 0'\n", gateway) +
		"\n[Install]\nWantedBy=multi-user.target\n"
}
