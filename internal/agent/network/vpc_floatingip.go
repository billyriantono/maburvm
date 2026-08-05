package network

import (
	"fmt"
	"net"
	"strings"
)

// Floating IPs for a VM inside a tenant VPC are configured entirely within that
// VPC's router namespace, not on the host.
//
// They cannot be done on the host: the host deliberately holds no route to a
// tenant's subnet (that is precisely what lets two tenants use 10.0.0.0/24), so
// a host-side DNAT to 10.0.0.10 would have nowhere to send the packet — and if a
// route were added, two tenants with the same guest address would collide again.
//
// So the namespace grows a second leg (eth-ext) on the public bridge, the
// floating IP lives there, and DNAT/SNAT happen inside. The namespace answers
// ARP for the address with its own MAC, so upstream reaches it directly.
//
// A VPC guest has no public identity of its own, so a floating IP here is always
// full 1:1: inbound DNAT plus outbound SNAT. The "inbound-only" mode exists for
// directly-bridged VMs that already hold a public address, which is not this
// case.

// vpcExtVeth is the root-namespace side of the VPC's public leg.
func vpcExtVeth(id8 string) string { return "mve" + id8 }

// ensureVPCPublicLeg attaches the router namespace to the public bridge, giving
// it a path to the internet that is independent of the private host uplink.
func ensureVPCPublicLeg(ns, id8, bridge string) error {
	mve := vpcExtVeth(id8)
	if !linkExists(mve) && !nsLinkExists(ns, "eth-ext") {
		if err := run("ip", "link", "add", mve, "type", "veth", "peer", "name", mve+"p"); err != nil {
			return fmt.Errorf("create veth %s: %w", mve, err)
		}
		if err := run("ip", "link", "set", mve+"p", "netns", ns); err != nil {
			return fmt.Errorf("move %sp into %s: %w", mve, ns, err)
		}
		if err := runNS(ns, "ip", "link", "set", mve+"p", "name", "eth-ext"); err != nil {
			return fmt.Errorf("rename to eth-ext: %w", err)
		}
	}
	if err := run("ip", "link", "set", mve, "master", bridge); err != nil {
		return fmt.Errorf("enslave %s to %s: %w", mve, bridge, err)
	}
	if err := run("ip", "link", "set", mve, "up"); err != nil {
		return fmt.Errorf("bring up %s: %w", mve, err)
	}
	return runNS(ns, "ip", "link", "set", "eth-ext", "up")
}

// AttachFloatingIPVPC points a floating IP at a guest inside a VPC.
// Idempotent, so the panel's reconcile pass restores it after a node reboot.
func (nm *NATManager) AttachFloatingIPVPC(vpcID, floatingIP, internalIP, bridge, gateway string) error {
	if err := validateFloatingArgs(floatingIP, internalIP); err != nil {
		return err
	}
	if bridge == "" {
		return fmt.Errorf("bridge is required to attach a floating IP in a VPC")
	}
	if net.ParseIP(gateway) == nil {
		return fmt.Errorf("invalid upstream gateway %q", gateway)
	}
	id8 := vpcShortID(vpcID)
	if len(id8) < 4 {
		return fmt.Errorf("vpc id %q has too little entropy", vpcID)
	}
	ns := vpcNetns(id8)
	if !netnsExists(ns) {
		return fmt.Errorf("VPC %s is not provisioned on this node", vpcID)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := ensureVPCPublicLeg(ns, id8, bridge); err != nil {
		return err
	}

	// The address is /32 and the upstream gateway is reached by an explicit
	// on-link route. Using the pool's real prefix instead would force the panel
	// to send it and would claim the whole segment inside every namespace.
	_ = runNS(ns, "ip", "addr", "add", floatingIP+"/32", "dev", "eth-ext")
	_ = runNS(ns, "ip", "route", "add", gateway, "dev", "eth-ext")

	// Egress for the whole VPC now leaves via the public leg as this address.
	// Replacing the default (rather than adding a second) keeps it deterministic:
	// the private host uplink stays as the path for a VPC with no floating IP.
	_ = runNS(ns, "ip", "route", "replace", "default", "via", gateway, "dev", "eth-ext")

	// Rules are replaced, never appended: re-pointing a floating IP at another
	// guest changes the rulespec, and a stale DNAT ahead of the new one would
	// keep sending traffic to the previous guest.
	if err := clearNSFloatingRules(ns, floatingIP); err != nil {
		return err
	}
	if err := runNS(ns, "iptables", "-t", "nat", "-A", "PREROUTING",
		"-d", floatingIP, "-j", "DNAT", "--to-destination", internalIP,
		"-m", "comment", "--comment", floatingComment(floatingIP)); err != nil {
		return fmt.Errorf("VPC floating IP DNAT: %w", err)
	}
	if err := runNS(ns, "iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", internalIP, "-o", "eth-ext", "-j", "SNAT", "--to-source", floatingIP,
		"-m", "comment", "--comment", floatingComment(floatingIP)); err != nil {
		return fmt.Errorf("VPC floating IP SNAT: %w", err)
	}

	return nil
}

// VPCFloatingMAC returns the MAC that answers ARP for floating IPs in this VPC,
// i.e. the router namespace's leg on the public bridge.
//
// The announcement itself is made from the ROOT namespace: the leg is bridged
// onto the same segment, so a frame injected there reaches the same L2 domain,
// and it lets the agent reuse its own raw-socket GARP instead of shelling out.
// The previous implementation called arping, which is installed on neither
// production node — so the announcement silently never happened, and moving a
// floating IP left upstream pointing at whoever held it before until the ARP
// cache expired.
func VPCFloatingMAC(vpcID string) (string, error) {
	ns := vpcNetns(vpcShortID(vpcID))
	out, err := nsOutput(ns, "cat", "/sys/class/net/eth-ext/address")
	if err != nil {
		return "", fmt.Errorf("read VPC public leg MAC: %w", err)
	}
	mac := strings.TrimSpace(out)
	if _, perr := net.ParseMAC(mac); perr != nil {
		return "", fmt.Errorf("VPC public leg has no usable MAC (%q)", mac)
	}
	return mac, nil
}

// DetachFloatingIPVPC removes a floating IP from a VPC's router namespace,
// leaving the VPC's own outbound path (the private host uplink) intact.
func (nm *NATManager) DetachFloatingIPVPC(vpcID, floatingIP string) error {
	if net.ParseIP(floatingIP) == nil {
		return fmt.Errorf("invalid floating IP %q", floatingIP)
	}
	id8 := vpcShortID(vpcID)
	ns := vpcNetns(id8)
	if !netnsExists(ns) {
		return nil // VPC already gone; nothing to undo
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := clearNSFloatingRules(ns, floatingIP); err != nil {
		return err
	}
	_ = runNS(ns, "ip", "addr", "del", floatingIP+"/32", "dev", "eth-ext")

	// Hand egress back to the private uplink, otherwise removing the last
	// floating IP would strand the VPC with a default route via an address it no
	// longer holds — the guests would lose internet with no obvious cause.
	if host := linkAddr(vpcUpVeth(id8)); host != "" {
		_ = runNS(ns, "ip", "route", "replace", "default", "via", host)
	}
	return nil
}

// clearNSFloatingRules removes every nat rule in the namespace tagged for this
// floating IP, in both PREROUTING and POSTROUTING.
//
// Deletion is by rule NUMBER, not by re-submitting the parsed rulespec. iptables
// prints the comment quoted ("maburvm-fip-1.2.3.4"), so splitting the -S output
// back into arguments carries those quotes into the match and nothing is ever
// deleted — which left the address attached and the detach failing outright.
func clearNSFloatingRules(ns, floatingIP string) error {
	comment := floatingComment(floatingIP)
	for _, chain := range []string{"PREROUTING", "POSTROUTING"} {
		out, err := nsOutput(ns, "iptables", "-t", "nat", "-S", chain)
		if err != nil {
			return fmt.Errorf("list %s in %s: %w", chain, ns, err)
		}
		// Only "-A <chain>" lines are numbered rules; the leading "-P" policy line
		// is not, so count positions as we go.
		pos := 0
		var victims []int
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "-A ") {
				continue
			}
			pos++
			if floatingRuleMatches(line, comment) {
				victims = append(victims, pos)
			}
		}
		// Highest first, so earlier positions stay valid as we delete.
		for i := len(victims) - 1; i >= 0; i-- {
			if err := runNS(ns, "iptables", "-t", "nat", "-D", chain, fmt.Sprintf("%d", victims[i])); err != nil {
				return fmt.Errorf("delete rule %d from %s in %s: %w", victims[i], chain, ns, err)
			}
		}
	}
	return nil
}
