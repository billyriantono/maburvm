// Package netrules holds the host-rule logic that needs neither libvirt nor a
// live iptables: what a rule must say, and what state the host must be in. It
// is kept apart from package network so it can be tested on any machine.
package netrules

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PortForwardRule builds the DNAT rulespec, restricting the source with -s when
// a specific CIDR is given (empty or "0.0.0.0/0" means any). The comment is
// stable per (vm, external port) so add/delete match.
func PortForwardRule(publishAddr, vmID string, externalPort int, internalIP string, internalPort int, protocol, sourceCIDR string) []string {
	rule := []string{}
	if sourceCIDR != "" && sourceCIDR != "0.0.0.0/0" {
		rule = append(rule, "-s", sourceCIDR)
	}
	// Only traffic addressed to the node's one publish address. "Any local
	// address" is not enough: floating IPs are bound on the host, and this
	// chain runs before theirs, so the forward would take the port on every
	// other customer's floating IP.
	rule = append(rule,
		"-d", publishAddr+"/32",
		"-p", protocol,
		"--dport", strconv.Itoa(externalPort),
		"-j", "DNAT",
		"--to-destination", fmt.Sprintf("%s:%d", internalIP, internalPort),
		"-m", "comment",
		"--comment", fmt.Sprintf("maburvm-vm-%s-port-%d", vmID, externalPort),
	)
	return rule
}

// ResolvePublishAddress picks the address port forwards answer on: the
// configured one, else the detected one. It refuses anything that is not a
// usable IPv4 address, and any floating IP: publishing forwards on a
// customer's address is the very capture this address exists to prevent.
func ResolvePublishAddress(configured, detected string, floating map[string]bool) (string, error) {
	candidate := strings.TrimSpace(configured)
	if candidate == "" {
		candidate = detected
	}
	ip := net.ParseIP(candidate).To4()
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", fmt.Errorf("%q is not a usable IPv4 address", candidate)
	}
	if floating[ip.String()] {
		return "", fmt.Errorf("%s is a floating IP attached on this node", ip)
	}
	return ip.String(), nil
}

// RuleDestinations returns the host addresses the given `iptables -S` lines
// match with -d.
func RuleDestinations(rules []string) map[string]bool {
	out := map[string]bool{}
	for _, rule := range rules {
		f := strings.Fields(rule)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-d" {
				out[strings.TrimSuffix(f[i+1], "/32")] = true
			}
		}
	}
	return out
}

// RescopedPortForward returns the rulespec that makes an `iptables -S` line of
// a port forward answer on addr only, and whether that differs from the line.
// Lines that are not port forwards are left alone.
func RescopedPortForward(line, addr string) ([]string, bool) {
	f := strings.Fields(line)
	if len(f) < 3 || f[0] != "-A" || !strings.Contains(line, "maburvm-vm-") || !strings.Contains(line, "-port-") {
		return nil, false
	}
	want := addr + "/32"
	spec := []string{"-d", want}
	changed := true
	for i := 2; i < len(f); i++ {
		switch {
		case f[i] == "-d" && i+1 < len(f):
			changed = f[i+1] != want
			i++
		case f[i] == "-m" && i+3 < len(f) && f[i+1] == "addrtype" && f[i+2] == "--dst-type" && f[i+3] == "LOCAL":
			i += 3
		default:
			spec = append(spec, strings.Trim(f[i], `"`))
		}
	}
	return spec, changed
}
