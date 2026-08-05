package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// A tenant VPC on a node is three things:
//
//	vbr<id>   a bridge in the ROOT namespace that guest NICs attach to. It
//	          deliberately carries NO address, which is what lets two tenants
//	          use the same subnet: with no address there is no route for that
//	          subnet in the host's table, so nothing can collide.
//	mvpc-<id> a router namespace holding the customer's gateway address and the
//	          VPC's NAT. Each namespace has its own routing table, so
//	          10.0.0.0/24 in tenant A and the identical 10.0.0.0/24 in tenant B
//	          never see each other.
//	two veths one pair bridges the namespace onto the guest bridge (eth-int,
//	          holding the gateway), the other is a private point-to-point link
//	          back to the host (eth-up) over which the VPC reaches the internet.
//
// Without the namespace the host would hold both tenants' gateways in one
// routing table and deliver traffic for BOTH to whichever bridge was created
// first — verified on a live node, and a cross-tenant misdelivery rather than a
// mere outage.
//
// The uplink is deliberately separate from floating IPs: a VPC has working
// outbound internet the moment it exists, before the customer has ordered any
// public address. A floating IP then adds *inbound* reachability, which is the
// part that is worth billing.

const (
	// vpcLinkSupernet carves the point-to-point host↔namespace links. Link-local
	// space is used on purpose: it is not routable, so it can never be confused
	// with a customer's own subnet, and RFC 1918 stays entirely theirs.
	vpcLinkSupernet = "169.254.128.0/17"
	// VPCPostChain holds the masquerade for those links, so a VPC's outbound
	// traffic leaves via the node's uplink.
	VPCPostChain = "MABURVM-VPC-POST"
)

// The "mv" prefix is deliberate: this node already carries unrelated interfaces
// starting with "vin" (vinfv1157 and friends), so a "vin"/"vbr" prefix would be
// ambiguous to anyone grepping interfaces during an incident.
//
// vpcShortID reduces a VPC id to 8 hex chars. Interface names are capped at 15
// bytes by the kernel (IFNAMSIZ), so a full UUID cannot be used.
func vpcShortID(vpcID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(vpcID) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
			if b.Len() == 8 {
				break
			}
		}
	}
	return b.String()
}

func vpcNetns(id8 string) string  { return "mvpc-" + id8 }
func vpcBridge(id8 string) string { return "mvb" + id8 }
func vpcIntVeth(id8 string) string {
	return "mvi" + id8
}                                 // root side, enslaved to the bridge
func vpcUpVeth(id8 string) string { return "mvu" + id8 } // root side of the uplink link

// validateVPC rejects a subnet the platform cannot safely host.
func validateVPC(subnet, gateway string) (*net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", subnet, err)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("subnet %q is not IPv4", subnet)
	}
	// Only RFC 1918. A public range would make the VPC's addresses collide with
	// real internet destinations for every guest in it, and link-local is
	// reserved for the host↔namespace links above.
	if !ip.IsPrivate() {
		return nil, fmt.Errorf("subnet %q must be a private range (10/8, 172.16/12, 192.168/16)", subnet)
	}
	if ones, _ := ipnet.Mask.Size(); ones > 29 {
		return nil, fmt.Errorf("subnet %q is too small to host guests (need /29 or larger)", subnet)
	}
	gw := net.ParseIP(gateway)
	if gw == nil || gw.To4() == nil {
		return nil, fmt.Errorf("invalid gateway %q", gateway)
	}
	if !ipnet.Contains(gw) {
		return nil, fmt.Errorf("gateway %s is outside subnet %s", gateway, subnet)
	}
	return ipnet, nil
}

// CreateVPC builds (or repairs) a VPC on this node and returns the bridge guests
// must attach to. Idempotent: the panel re-applies it to restore VPCs after a
// node reboot, which wipes namespaces and veths entirely.
func (nm *NATManager) CreateVPC(vpcID, subnet, gateway string) (string, error) {
	ipnet, err := validateVPC(subnet, gateway)
	if err != nil {
		return "", err
	}
	id8 := vpcShortID(vpcID)
	if len(id8) < 4 {
		return "", fmt.Errorf("vpc id %q has too little entropy for an interface name", vpcID)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	ns, br := vpcNetns(id8), vpcBridge(id8)
	prefix, _ := ipnet.Mask.Size()

	// Guest bridge — no address, on purpose (see the file comment).
	if !linkExists(br) {
		if err := run("ip", "link", "add", br, "type", "bridge"); err != nil {
			return "", fmt.Errorf("create bridge %s: %w", br, err)
		}
	}
	if err := run("ip", "link", "set", br, "up"); err != nil {
		return "", fmt.Errorf("bring up %s: %w", br, err)
	}

	if !netnsExists(ns) {
		if err := run("ip", "netns", "add", ns); err != nil {
			return "", fmt.Errorf("create netns %s: %w", ns, err)
		}
	}
	_ = runNS(ns, "ip", "link", "set", "lo", "up")

	// Internal link: namespace onto the guest bridge, holding the gateway.
	vin := vpcIntVeth(id8)
	if !linkExists(vin) && !nsLinkExists(ns, "eth-int") {
		if err := run("ip", "link", "add", vin, "type", "veth", "peer", "name", vin+"p"); err != nil {
			return "", fmt.Errorf("create veth %s: %w", vin, err)
		}
		if err := run("ip", "link", "set", vin+"p", "netns", ns); err != nil {
			return "", fmt.Errorf("move %sp into %s: %w", vin, ns, err)
		}
		if err := runNS(ns, "ip", "link", "set", vin+"p", "name", "eth-int"); err != nil {
			return "", fmt.Errorf("rename to eth-int: %w", err)
		}
	}
	_ = run("ip", "link", "set", vin, "master", br)
	_ = run("ip", "link", "set", vin, "up")
	_ = runNS(ns, "ip", "addr", "add", fmt.Sprintf("%s/%d", gateway, prefix), "dev", "eth-int")
	if err := runNS(ns, "ip", "link", "set", "eth-int", "up"); err != nil {
		return "", fmt.Errorf("bring up eth-int: %w", err)
	}

	// Uplink: private point-to-point link back to the host.
	vup := vpcUpVeth(id8)
	hostIP, nsIP, err := nm.vpcLinkAddrs(vup)
	if err != nil {
		return "", err
	}
	if !linkExists(vup) && !nsLinkExists(ns, "eth-up") {
		if err := run("ip", "link", "add", vup, "type", "veth", "peer", "name", vup+"p"); err != nil {
			return "", fmt.Errorf("create veth %s: %w", vup, err)
		}
		if err := run("ip", "link", "set", vup+"p", "netns", ns); err != nil {
			return "", fmt.Errorf("move %sp into %s: %w", vup, ns, err)
		}
		if err := runNS(ns, "ip", "link", "set", vup+"p", "name", "eth-up"); err != nil {
			return "", fmt.Errorf("rename to eth-up: %w", err)
		}
	}
	_ = run("ip", "addr", "add", hostIP+"/30", "dev", vup)
	if err := run("ip", "link", "set", vup, "up"); err != nil {
		return "", fmt.Errorf("bring up %s: %w", vup, err)
	}
	_ = runNS(ns, "ip", "addr", "add", nsIP+"/30", "dev", "eth-up")
	if err := runNS(ns, "ip", "link", "set", "eth-up", "up"); err != nil {
		return "", fmt.Errorf("bring up eth-up: %w", err)
	}
	_ = runNS(ns, "ip", "route", "add", "default", "via", hostIP)
	_ = runNS(ns, "sysctl", "-qw", "net.ipv4.ip_forward=1")

	// NAT: guests leave the namespace as its uplink address, and the host then
	// masquerades that link out of the node's own uplink.
	// -C first: appending unconditionally would stack a duplicate masquerade on
	// every reconcile pass, and the panel reconciles continuously.
	if runNS(ns, "iptables", "-t", "nat", "-C", "POSTROUTING", "-s", subnet, "-o", "eth-up", "-j", "MASQUERADE") != nil {
		if err := runNS(ns, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", subnet, "-o", "eth-up", "-j", "MASQUERADE"); err != nil {
			return "", fmt.Errorf("namespace masquerade: %w", err)
		}
	}
	if err := nm.ensureVPCHostNAT(hostIP, id8); err != nil {
		return "", err
	}

	return br, nil
}

// DeleteVPC removes a VPC's namespace, bridge and links. Deleting the namespace
// takes its interfaces (and the veth peers) with it.
func (nm *NATManager) DeleteVPC(vpcID string) error {
	id8 := vpcShortID(vpcID)
	if len(id8) < 4 {
		return fmt.Errorf("vpc id %q has too little entropy", vpcID)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	ns := vpcNetns(id8)
	// Read the link address before tearing the interface down, so the matching
	// host masquerade can be removed rather than left behind.
	hostIP := linkAddr(vpcUpVeth(id8))

	_ = run("ip", "netns", "del", ns)
	_ = run("ip", "link", "del", vpcIntVeth(id8))
	_ = run("ip", "link", "del", vpcUpVeth(id8))
	_ = run("ip", "link", "del", vpcBridge(id8))
	if hostIP != "" {
		_ = nm.ipt.Delete(NATTable, VPCPostChain, vpcHostNATRule(hostIP, id8)...)
	}
	return nil
}

// ensureVPCHostNAT masquerades a VPC's host↔namespace link out of the node.
func (nm *NATManager) ensureVPCHostNAT(hostIP, id8 string) error {
	chains, err := nm.ipt.ListChains(NATTable)
	if err != nil {
		return fmt.Errorf("list nat chains: %w", err)
	}
	found := false
	for _, c := range chains {
		if c == VPCPostChain {
			found = true
			break
		}
	}
	if !found {
		if err := nm.ipt.NewChain(NATTable, VPCPostChain); err != nil {
			return fmt.Errorf("create %s: %w", VPCPostChain, err)
		}
	}
	ok, err := nm.ipt.Exists(NATTable, PostroutingChain, "-j", VPCPostChain)
	if err != nil {
		return fmt.Errorf("check POSTROUTING jump: %w", err)
	}
	if !ok {
		if err := nm.ipt.Append(NATTable, PostroutingChain, "-j", VPCPostChain); err != nil {
			return fmt.Errorf("add POSTROUTING jump: %w", err)
		}
	}
	return nm.appendIfMissing(VPCPostChain, vpcHostNATRule(hostIP, id8))
}

func vpcHostNATRule(hostIP, id8 string) []string {
	// The whole /30 is this VPC's link, so matching the network covers both ends.
	// This MUST mask properly rather than zero the last octet: the /30s are laid
	// out back to back (…0.0, …0.4, …0.8), so zeroing turns every link after the
	// first into …0.0/30 — the second VPC then gets no masquerade of its own and
	// silently has no outbound internet. Observed exactly that on a live node.
	network := hostIP
	if ip := net.ParseIP(hostIP).To4(); ip != nil {
		network = ip.Mask(net.CIDRMask(30, 32)).String()
	}
	return []string{
		"-s", network + "/30",
		"-j", "MASQUERADE",
		"-m", "comment", "--comment", "maburvm-vpc-" + id8,
	}
}

// vpcLinkAddrs returns the host and namespace ends of this VPC's /30, reusing
// the existing one when the link is already up so a repair keeps its address.
func (nm *NATManager) vpcLinkAddrs(vup string) (string, string, error) {
	if cur := linkAddr(vup); cur != "" {
		return cur, peerOfLinkAddr(cur), nil
	}
	_, super, err := net.ParseCIDR(vpcLinkSupernet)
	if err != nil {
		return "", "", err
	}
	used := usedLinkAddrs()
	base := super.IP.To4()
	// /30s are laid out back to back: .0 network, .1 host, .2 namespace, .3 bcast.
	for i := 0; i < 8192; i++ {
		off := i * 4
		ip := net.IPv4(base[0], base[1], base[2]+byte(off/256), base[3]+byte(off%256)).To4()
		host := net.IPv4(ip[0], ip[1], ip[2], ip[3]+1).To4().String()
		if !used[host] {
			return host, net.IPv4(ip[0], ip[1], ip[2], ip[3]+2).To4().String(), nil
		}
	}
	return "", "", fmt.Errorf("no free VPC link subnet left in %s", vpcLinkSupernet)
}

// usedLinkAddrs collects the host-side link addresses already handed out.
func usedLinkAddrs() map[string]bool {
	used := map[string]bool{}
	out, err := exec.Command("ip", "-4", "-o", "addr", "show").CombinedOutput()
	if err != nil {
		return used
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i < len(f)-1; i++ {
			if f[i] == "inet" {
				addr := strings.SplitN(f[i+1], "/", 2)[0]
				if strings.HasPrefix(addr, "169.254.") {
					used[addr] = true
				}
			}
		}
	}
	return used
}

// peerOfLinkAddr maps a /30's host end (.1) to its namespace end (.2).
func peerOfLinkAddr(hostIP string) string {
	ip := net.ParseIP(hostIP).To4()
	if ip == nil {
		return ""
	}
	return net.IPv4(ip[0], ip[1], ip[2], ip[3]+1).To4().String()
}

func linkAddr(dev string) string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", dev).CombinedOutput()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i := 0; i < len(f)-1; i++ {
		if f[i] == "inet" {
			return strings.SplitN(f[i+1], "/", 2)[0]
		}
	}
	return ""
}

func linkExists(dev string) bool {
	return exec.Command("ip", "link", "show", "dev", dev).Run() == nil
}

func netnsExists(ns string) bool {
	out, err := exec.Command("ip", "netns", "list").CombinedOutput()
	if err != nil {
		return false
	}
	// "ip netns list" prints "name (id: N)"; compare the first field exactly so
	// mvpc-abc does not match mvpc-abcdef.
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == ns {
			return true
		}
	}
	return false
}

func nsLinkExists(ns, dev string) bool {
	return exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", dev).Run() == nil
}

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runNS(ns string, args ...string) error {
	return run(append([]string{"ip", "netns", "exec", ns}, args...)...)
}

// VPCNetnsName exposes the router namespace name for diagnostics.
func VPCNetnsName(vpcID string) string { return vpcNetns(vpcShortID(vpcID)) }
