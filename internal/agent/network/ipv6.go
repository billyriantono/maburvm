package network

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// IPv6Delegation is a routed prefix the host must forward to a guest: the
// prefix itself, the guest NIC's MAC (its EUI-64 link-local is the next hop)
// and the bridge both sit on.
type IPv6Delegation struct {
	Prefix string // "2001:db8:20:5::/64"
	MAC    string
	Bridge string
	// OnLink marks an address on a subnet shared with other VMs (Prefix is
	// then that one address as a /128). The guest answers for it itself and
	// the subnet's own router is the gateway, so the node adds no route and
	// no neighbour entries; only the firewall and the guest filter use it.
	OnLink bool `json:",omitempty"`
	// Extra are further addresses inside Prefix the guest holds. The prefix
	// is routed whole; they only need their own NDP proxy entries.
	Extra []string `json:",omitempty"`
}

// Same reports whether d and o ask for the same host state.
func (d IPv6Delegation) Same(o IPv6Delegation) bool {
	return d.Prefix == o.Prefix && d.MAC == o.MAC && d.Bridge == o.Bridge && d.OnLink == o.OnLink && slices.Equal(d.Extra, o.Extra)
}

// EUI64LinkLocal derives the guest's fe80:: address from its MAC the way
// every stock kernel does (flip the U/L bit, insert ff:fe), e.g.
// 52:54:00:12:34:56 -> fe80::5054:ff:fe12:3456. The guest is told to keep
// EUI-64 link-local generation, so this is the address the host's route and
// neighbour entry must point at.
func EUI64LinkLocal(mac string) (string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid MAC %q", mac)
	}
	ip := make(net.IP, net.IPv6len)
	ip[0], ip[1] = 0xfe, 0x80
	ip[8] = hw[0] ^ 0x02
	ip[9], ip[10] = hw[1], hw[2]
	ip[11], ip[12] = 0xff, 0xfe
	ip[13], ip[14], ip[15] = hw[3], hw[4], hw[5]
	return ip.String(), nil
}

// ipv6RouteCommands are the `ip` invocations that make the host forward d's
// prefix to the guest: a permanent neighbour entry (the guest never answers
// NS - its nwfilter drops NA) and the route via that link-local. Both are
// `replace`, so re-running them is a no-op and a changed MAC just moves them.
// The proxy entries answer NS for the guest's <prefix>::1 and each further
// address on the bridge, for upstreams that treat the whole pool as on-link
// instead of routing it to the host. They are inert unless
// net.ipv6.conf.<bridge>.proxy_ndp=1.
func ipv6RouteCommands(d IPv6Delegation, linkLocal string) [][]string {
	cmds := [][]string{
		{"ip", "-6", "neigh", "replace", linkLocal, "lladdr", strings.ToLower(d.MAC), "dev", d.Bridge, "nud", "permanent"},
		{"ip", "-6", "route", "replace", d.Prefix, "via", linkLocal, "dev", d.Bridge},
	}
	for _, a := range d.proxied() {
		cmds = append(cmds, []string{"ip", "-6", "neigh", "replace", "proxy", a, "dev", d.Bridge})
	}
	return cmds
}

// ipv6RouteDeleteCommands undo ipv6RouteCommands.
func ipv6RouteDeleteCommands(d IPv6Delegation, linkLocal string) [][]string {
	var cmds [][]string
	for _, a := range d.proxied() {
		cmds = append(cmds, []string{"ip", "-6", "neigh", "del", "proxy", a, "dev", d.Bridge})
	}
	return append(cmds,
		[]string{"ip", "-6", "route", "del", d.Prefix, "via", linkLocal, "dev", d.Bridge},
		[]string{"ip", "-6", "neigh", "del", linkLocal, "dev", d.Bridge},
	)
}

// sameRoute reports whether only the further addresses differ, so the route
// can stay up while the proxy entries change.
func (d IPv6Delegation) sameRoute(o IPv6Delegation) bool {
	return d.Prefix == o.Prefix && d.MAC == o.MAC && d.Bridge == o.Bridge && d.OnLink == o.OnLink
}

// removeStaleProxies drops the proxy entries prev had that next does not.
func removeStaleProxies(prev, next IPv6Delegation) {
	if prev.OnLink {
		return
	}
	keep := next.proxied()
	for _, a := range prev.proxied() {
		if !slices.Contains(keep, a) {
			if out, err := exec.Command("ip", "-6", "neigh", "del", "proxy", a, "dev", prev.Bridge).CombinedOutput(); err != nil && !strings.Contains(string(out), "No such") {
				log.Printf("[NetworkManager] WARNING: removing NDP proxy %s: %s", a, strings.TrimSpace(string(out)))
			}
		}
	}
}

// proxied are the addresses answered for by NDP proxy: ::1, then every valid
// further address inside the prefix.
func (d IPv6Delegation) proxied() []string {
	out := []string{guestIPv6(d.Prefix)}
	_, n, err := net.ParseCIDR(d.Prefix)
	if err != nil {
		return out
	}
	for _, a := range d.Extra {
		if ip := net.ParseIP(a); ip != nil && ip.To4() == nil && n.Contains(ip) && !slices.Contains(out, ip.String()) {
			out = append(out, ip.String())
		}
	}
	return out
}

// guestIPv6 is the address the guest takes in its prefix: <prefix>::1.
// Callers have already validated the prefix.
func guestIPv6(prefix string) string {
	_, n, _ := net.ParseCIDR(prefix)
	ip := n.IP.To16()
	ip[15] |= 1
	return ip.String()
}

func (d IPv6Delegation) validate() (string, error) {
	if _, _, err := net.ParseCIDR(d.Prefix); err != nil {
		return "", fmt.Errorf("invalid IPv6 prefix %q: %w", d.Prefix, err)
	}
	if d.Bridge == "" {
		return "", fmt.Errorf("bridge is required to route %s", d.Prefix)
	}
	return EUI64LinkLocal(d.MAC)
}

// applyIPv6Route installs the neighbour entry and route for d. Idempotent.
func applyIPv6Route(d IPv6Delegation) (string, error) {
	if d.OnLink {
		return "the shared subnet", nil
	}
	ll, err := d.validate()
	if err != nil {
		return "", err
	}
	for _, args := range ipv6RouteCommands(d, ll) {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return ll, nil
}

// removeIPv6Route tears down what applyIPv6Route installed. A route or
// neighbour that is already gone is not an error.
func removeIPv6Route(d IPv6Delegation) error {
	if d.OnLink {
		return nil
	}
	ll, err := d.validate()
	if err != nil {
		return err
	}
	var errs []string
	for _, args := range ipv6RouteDeleteCommands(d, ll) {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such") {
			errs = append(errs, fmt.Sprintf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(out))))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ipv6StatePath is where routed prefixes are persisted (MABURVM_DATA_DIR, like the
// agent's other state; default /var/lib/maburvm).
func ipv6StatePath() string {
	dir := os.Getenv("MABURVM_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/maburvm"
	}
	return filepath.Join(dir, "ipv6-delegations.json")
}

// loadIPv6Delegations reads the persisted prefixes; a missing or unreadable file
// is an empty map (logged when unreadable).
func loadIPv6Delegations(path string) map[string]IPv6Delegation {
	out := map[string]IPv6Delegation{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		log.Printf("[NetworkManager] WARNING: ignoring unreadable %s: %v", path, err)
		return map[string]IPv6Delegation{}
	}
	return out
}

// saveIPv6Delegations writes the map atomically (temp file + rename).
func saveIPv6Delegations(path string, d map[string]IPv6Delegation) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
