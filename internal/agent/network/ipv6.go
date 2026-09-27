package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// IPv6Delegation is a routed prefix the host must forward to a guest: the
// prefix itself, the guest NIC's MAC (its EUI-64 link-local is the next hop)
// and the bridge both sit on.
type IPv6Delegation struct {
	Prefix string // "2001:db8:20:5::/64"
	MAC    string
	Bridge string
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
func ipv6RouteCommands(d IPv6Delegation, linkLocal string) [][]string {
	return [][]string{
		{"ip", "-6", "neigh", "replace", linkLocal, "lladdr", strings.ToLower(d.MAC), "dev", d.Bridge, "nud", "permanent"},
		{"ip", "-6", "route", "replace", d.Prefix, "via", linkLocal, "dev", d.Bridge},
	}
}

// ipv6RouteDeleteCommands undo ipv6RouteCommands.
func ipv6RouteDeleteCommands(d IPv6Delegation, linkLocal string) [][]string {
	return [][]string{
		{"ip", "-6", "route", "del", d.Prefix, "via", linkLocal, "dev", d.Bridge},
		{"ip", "-6", "neigh", "del", linkLocal, "dev", d.Bridge},
	}
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

// ipv6FirewallSpecs mirrors applyRuleInternal for ip6tables, with the VM's
// delegated prefix in place of its address. sorted must already be in
// priority order (the caller inserts each spec at position 1, exactly like
// IPv4). Rules scoped to an IPv4 source can never match v6 traffic and are
// skipped; IPv6 source CIDRs are honoured; "icmp" means ICMPv6 here.
// Returns the per-rule specs and the trailing default-drop spec.
func ipv6FirewallSpecs(vmID, prefix string, sorted []FirewallRule) (specs [][]string, defaultDrop []string) {
	for _, rule := range sorted {
		var spec []string
		switch rule.Protocol {
		case "", "all":
		case "icmp":
			spec = append(spec, "-p", "icmpv6")
		default:
			spec = append(spec, "-p", rule.Protocol)
		}
		if src := rule.SourceIP; src != "" && src != "0.0.0.0/0" && src != "::/0" {
			if ip, _, err := net.ParseCIDR(src); err == nil && ip.To4() != nil {
				continue // IPv4-only source: cannot apply to v6
			} else if ip := net.ParseIP(src); ip != nil && ip.To4() != nil {
				continue
			}
			spec = append(spec, "-s", src)
		}
		if rule.Direction == "inbound" {
			spec = append(spec, "-d", prefix)
		} else {
			spec = append(spec, "-s", prefix)
		}
		if rule.PortRange != "" && (rule.Protocol == "tcp" || rule.Protocol == "udp") {
			portSpec, multi, err := normalizePortSpec(rule.PortRange)
			if err != nil {
				continue // validated on the v4 pass; never reached
			}
			if multi {
				spec = append(spec, "-m", "multiport", "--dports", portSpec)
			} else {
				spec = append(spec, "--dport", portSpec)
			}
		}
		if rule.Action == "allow" {
			spec = append(spec, "-j", "ACCEPT")
		} else {
			spec = append(spec, "-j", "DROP")
		}
		spec = append(spec, "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-rule-%s", vmID, rule.ID))
		specs = append(specs, spec)
	}
	defaultDrop = []string{"-d", prefix, "-j", "DROP", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-default-drop", vmID)}
	return specs, defaultDrop
}
