package guestos

import (
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
)

// GuestAddr is an IPv4 address a guest holds besides its primary one. It goes
// on the same card, after the primary, so the primary stays the source of the
// guest's own connections.
type GuestAddr struct {
	Address string // IPv4, no prefix
	Prefix  int
}

// resolvers is the list a guest is given: dns (the defaults when empty), then
// v6. Callers validated both, so each is written back in parsed form.
func resolvers(dns, v6 []string) []string {
	if len(dns) == 0 {
		dns = models.DefaultIPv4DNS
	}
	var out []string
	for _, d := range append(append([]string{}, dns...), v6...) {
		if ip := net.ParseIP(d); ip != nil && !slices.Contains(out, ip.String()) {
			out = append(out, ip.String())
		}
	}
	return out
}

func validateDNS(list []string) error {
	for _, d := range list {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
	}
	return nil
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

// LinuxNetwork is the addressing a Linux guest is given.
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
	// IPv6Extra are further addresses in the same prefix, without a length.
	IPv6Extra []string
	// DNS are the resolvers; empty keeps models.DefaultIPv4DNS.
	DNS []string
	// NICs are the guest's further cards. The default route stays on the
	// first one.
	NICs []GuestNIC
}

// GuestNIC is a further card of a guest and the IPv4 addresses on it.
type GuestNIC struct {
	MAC       string
	Addresses []GuestAddr
}

// Validate checks every value that is written into a guest's config, so
// none of it reaches a file or a shell as anything but an address.
func (n LinuxNetwork) Validate() error {
	if _, err := net.ParseMAC(n.MAC); err != nil {
		return fmt.Errorf("invalid MAC address %q", n.MAC)
	}
	if ip := net.ParseIP(n.Address); ip == nil || ip.To4() == nil || n.Prefix < 1 || n.Prefix > 32 {
		return fmt.Errorf("invalid IPv4 address %q/%d", n.Address, n.Prefix)
	}
	if n.Gateway != "" {
		if ip := net.ParseIP(n.Gateway); ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid gateway %q", n.Gateway)
		}
	}
	if err := ValidateExtra(n.Extra); err != nil {
		return err
	}
	if err := validateDNS(append(append([]string{}, n.DNS...), n.IPv6DNS...)); err != nil {
		return err
	}
	if n.IPv6Address != "" {
		if ip, _, err := net.ParseCIDR(n.IPv6Address); err != nil || ip.To4() != nil {
			return fmt.Errorf("invalid IPv6 address %q", n.IPv6Address)
		}
		if ip := net.ParseIP(n.IPv6Gateway); n.IPv6Gateway != "" && ip == nil {
			return fmt.Errorf("invalid IPv6 gateway %q", n.IPv6Gateway)
		}
		for _, a := range n.IPv6Extra {
			if ip := net.ParseIP(a); ip == nil || ip.To4() != nil {
				return fmt.Errorf("invalid IPv6 address %q", a)
			}
		}
	}
	for _, c := range n.NICs {
		if _, err := net.ParseMAC(c.MAC); err != nil {
			return fmt.Errorf("invalid MAC address %q", c.MAC)
		}
		if err := ValidateExtra(c.Addresses); err != nil {
			return err
		}
	}
	return nil
}

// ipv6Len is the prefix length of the guest's IPv6 address.
func (n LinuxNetwork) ipv6Len() int {
	_, p, err := net.ParseCIDR(n.IPv6Address)
	if err != nil {
		return 64
	}
	ones, _ := p.Mask.Size()
	return ones
}

// ipv6Gateway is the guest's IPv6 default route, fe80::1 unless told.
func (n LinuxNetwork) ipv6Gateway() string {
	if n.IPv6Gateway == "" {
		return models.IPv6LinkLocalGateway
	}
	return n.IPv6Gateway
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
		for _, a := range n.IPv6Extra {
			fmt.Fprintf(&b, "Address=%s/%d\n", a, n.ipv6Len())
		}
	}
	for _, d := range resolvers(n.DNS, n.IPv6DNS) {
		fmt.Fprintf(&b, "DNS=%s\n", d)
	}
	return b.String()
}

// NetworkdNICUnit is the unit of a further card: its addresses, no route.
func NetworkdNICUnit(c GuestNIC) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Match]\nMACAddress=%s\n\n[Network]\n", strings.ToLower(c.MAC))
	for _, a := range c.Addresses {
		fmt.Fprintf(&b, "Address=%s/%d\n", a.Address, a.Prefix)
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
