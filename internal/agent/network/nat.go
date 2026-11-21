package network

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"

	"github.com/maburvm/panel/internal/agent/netrules"
)

const (
	// NATTable is the iptables nat table
	NATTable = "nat"
	// FilterTable is the iptables filter table
	FilterTable = "filter"
	// PostroutingChain is the POSTROUTING chain in nat table
	PostroutingChain = "POSTROUTING"
	// PreroutingChain is the PREROUTING chain in nat table
	PreroutingChain = "PREROUTING"
	// ForwardChain is the FORWARD chain in filter table
	ForwardChain = "FORWARD"
	// MaburVMChain is the custom chain for MaburVM rules
	MaburVMChain = "MABURVM-NAT"
	// natAddressEnv overrides the node address port forwards are published on.
	// Unset, it is the address the node's default route sends from.
	natAddressEnv = "MABURVM_NAT_ADDRESS"
)

// NATManager handles NAT and port forwarding operations
type NATManager struct {
	ipt *iptables.IPTables
	mu  sync.RWMutex
	// portForwards tracks active port forwards: map[vmID]map[externalPort]{internalIP, internalPort}
	portForwards map[string]map[int]portForwardEntry
	// publishAddr is the one host address port forwards answer on. A forward
	// must never match "any local address": floating IPs are local to the host
	// too, and belong to other customers. Empty while no address could be
	// determined, in which case no forward is installed.
	publishAddr string
}

type portForwardEntry struct {
	internalIP   string
	internalPort int
	protocol     string
	sourceCIDR   string
}

// NewNATManager creates a new NATManager instance
func NewNATManager() (*NATManager, error) {
	ipt, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4))
	if err != nil {
		return nil, fmt.Errorf("failed to create iptables client: %w", err)
	}

	nm := &NATManager{
		ipt:          ipt,
		portForwards: make(map[string]map[int]portForwardEntry),
	}

	// Ensure our custom chain exists
	if err := nm.ensureChain(); err != nil {
		return nil, err
	}

	if err := nm.ensurePublishAddr(); err != nil {
		log.Printf("[NATManager] WARNING: %v. Port forwards are NOT applied until it is known", err)
	}

	return nm, nil
}

// PublishAddress is the node address port forwards answer on, empty when none.
func (nm *NATManager) PublishAddress() string {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	// The panel polls this, so an address that was missing at start (no route
	// yet) is settled, and the old forwards rescoped, without waiting for a VM.
	_ = nm.ensurePublishAddr()
	return nm.publishAddr
}

// ensurePublishAddr settles publishAddr if it is not known yet, and brings the
// forwards already in the chain in line with it. Tried again on later use: at
// boot the agent can start before the default route exists. Assumes the lock
// is held (or that nothing else has the manager yet).
func (nm *NATManager) ensurePublishAddr() error {
	if nm.publishAddr != "" {
		return nil
	}
	floating, _ := nm.ipt.List(NATTable, FloatingIPChain) // absent until the first floating IP
	addr, err := netrules.ResolvePublishAddress(os.Getenv(natAddressEnv), defaultRouteSource(), netrules.RuleDestinations(floating))
	if err == nil && !hostHasAddress(addr) {
		// Rescoping to an address the node does not hold would stop every forward.
		err = fmt.Errorf("%s is not an address of this node", addr)
	}
	if err != nil {
		return fmt.Errorf("no node address to publish port forwards on (%w); set %s to the node's public IPv4 address", err, natAddressEnv)
	}
	nm.publishAddr = addr
	log.Printf("[NATManager] port forwards are published on %s", addr)
	nm.rescopePortForwards()
	return nil
}

// hostHasAddress reports whether ip is assigned to an interface of this host.
func hostHasAddress(ip string) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.String() == ip {
			return true
		}
	}
	return false
}

// defaultRouteSource is the address the kernel sends from on the default route.
// Dialling UDP sends nothing; it only asks the routing table.
func defaultRouteSource() string {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// rescopePortForwards rewrites, in place, forwards an older agent installed.
// Those matched every address local to the host, so they also took the port
// on other customers' floating IPs. Iptables rules outlive the agent, so
// without this an upgraded node keeps the old rules until each VM is next
// re-applied. Replacing by position keeps every forward answering throughout.
func (nm *NATManager) rescopePortForwards() {
	rules, err := nm.ipt.List(NATTable, MaburVMChain)
	if err != nil {
		log.Printf("[NATManager] WARNING: could not list port forwards to rescope: %v", err)
		return
	}
	// rules[0] is the "-N <chain>" line, so rule i is at position i.
	for i := 1; i < len(rules); i++ {
		spec, changed := netrules.RescopedPortForward(rules[i], nm.publishAddr)
		if !changed {
			continue
		}
		if err := nm.ipt.Replace(NATTable, MaburVMChain, i, spec...); err != nil {
			log.Printf("[NATManager] WARNING: could not rescope port forward %q: %v", rules[i], err)
			continue
		}
		log.Printf("[NATManager] port forward rescoped to %s: %s", nm.publishAddr, strings.Join(spec, " "))
	}
}

// ensureChain creates the custom MaburVM chain if it doesn't exist
func (nm *NATManager) ensureChain() error {
	// Check if chain exists in nat table
	chains, err := nm.ipt.ListChains(NATTable)
	if err != nil {
		return fmt.Errorf("failed to list chains: %w", err)
	}

	chainExists := false
	for _, chain := range chains {
		if chain == MaburVMChain {
			chainExists = true
			break
		}
	}

	if !chainExists {
		// Create the chain
		if err := nm.ipt.NewChain(NATTable, MaburVMChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", MaburVMChain, err)
		}

		// Jump to our chain from PREROUTING for DNAT rules
		exists, err := nm.ipt.Exists(NATTable, PreroutingChain, "-j", MaburVMChain)
		if err != nil {
			return fmt.Errorf("failed to check PREROUTING jump: %w", err)
		}
		if !exists {
			if err := nm.ipt.Insert(NATTable, PreroutingChain, 1, "-j", MaburVMChain); err != nil {
				return fmt.Errorf("failed to add PREROUTING jump: %w", err)
			}
		}
	}

	return nil
}

// isPrivateAddress reports whether an address needs the host to translate it to
// reach the internet.
//
// Anything routable does not: it is the VM's own identity on the wire, and
// rewriting it breaks the VM while leaving inbound connections working, which is
// what makes the fault so hard to recognise.
func isPrivateAddress(addr string) bool {
	ip := net.ParseIP(strings.TrimSpace(strings.SplitN(addr, "/", 2)[0]))
	if ip == nil {
		// Unparseable: masquerade, matching the previous behaviour. An address
		// we cannot classify is more likely a private one from a managed network
		// than a public allocation.
		return true
	}
	// Carrier-grade NAT space (100.64.0.0/10) is not covered by IsPrivate but is
	// equally unroutable from the internet.
	cgnat := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	if cgnat.Contains(ip) {
		return true
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// SetupNAT sets up MASQUERADE for a VM's internal IP
// This allows the VM to access external networks through NAT
func (nm *NATManager) SetupNAT(vmID string, internalIP string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Validate IP address
	if internalIP == "" {
		return fmt.Errorf("internal IP cannot be empty")
	}

	// Masquerade ONLY a VM whose address is private.
	//
	// A VM holding a routable public address is bridged onto the uplink and is
	// its own identity on the wire; rewriting its source to the host's address
	// sends packets out of a VLAN carrying a source from a different subnet,
	// which the upstream router discards. The symptom is precise and confusing:
	// replies to inbound connections keep working, because their conntrack entry
	// was created by the inbound packet and carries no NAT, while every
	// connection the VM itself opens dies silently. Observed on a live VM with
	// 161 packets counted against the rule and no outbound connectivity.
	if isPrivateAddress(internalIP) {
		rule := []string{"-s", internalIP, "-j", "MASQUERADE", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s", vmID)}

		exists, err := nm.ipt.Exists(NATTable, PostroutingChain, rule...)
		if err != nil {
			return fmt.Errorf("failed to check MASQUERADE rule: %w", err)
		}

		if !exists {
			if err := nm.ipt.Append(NATTable, PostroutingChain, rule...); err != nil {
				return fmt.Errorf("failed to add MASQUERADE rule: %w", err)
			}
		}
	} else {
		// A public address may carry a masquerade left by an earlier version, or
		// by a private address previously assigned to this VM. Remove it, so a
		// VM that was broken by it recovers on the next network config push
		// rather than needing someone to notice and clear it by hand.
		stale := []string{"-s", internalIP, "-j", "MASQUERADE", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s", vmID)}
		if ok, cerr := nm.ipt.Exists(NATTable, PostroutingChain, stale...); cerr == nil && ok {
			if derr := nm.ipt.Delete(NATTable, PostroutingChain, stale...); derr != nil {
				log.Printf("[NATManager] WARNING: could not remove masquerade for public address %s: %v", internalIP, derr)
			} else {
				log.Printf("[NATManager] removed masquerade for public address %s (a routable VM must egress as itself)", internalIP)
			}
		}
	}

	// Enable IP forwarding if not already enabled (this is a system-wide setting)
	// In production, this should be done during agent initialization
	// For now, we just ensure the iptables rules are in place

	// Add FORWARD rule to allow traffic from/to this VM
	forwardRule := []string{"-s", internalIP, "-j", "ACCEPT", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-out", vmID)}
	exists, err := nm.ipt.Exists(FilterTable, ForwardChain, forwardRule...)
	if err != nil {
		return fmt.Errorf("failed to check FORWARD rule: %w", err)
	}
	if !exists {
		if err := nm.ipt.Append(FilterTable, ForwardChain, forwardRule...); err != nil {
			return fmt.Errorf("failed to add FORWARD rule: %w", err)
		}
	}

	// Add FORWARD rule for incoming traffic
	forwardRuleIn := []string{"-d", internalIP, "-j", "ACCEPT", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-in", vmID)}
	exists, err = nm.ipt.Exists(FilterTable, ForwardChain, forwardRuleIn...)
	if err != nil {
		return fmt.Errorf("failed to check FORWARD rule: %w", err)
	}
	if !exists {
		if err := nm.ipt.Append(FilterTable, ForwardChain, forwardRuleIn...); err != nil {
			return fmt.Errorf("failed to add FORWARD rule: %w", err)
		}
	}

	return nil
}

// RemoveNAT removes NAT rules for a VM
func (nm *NATManager) RemoveNAT(vmID string, internalIP string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Port forwards go first and are found by their tag, not rebuilt from
	// memory: the deletes below return early when a rule they expect is not
	// there (a VM on a public bridge has no MASQUERADE rule), and that used to
	// leave every forward in place. Reading the chain also catches forwards
	// added before the agent last restarted.
	nm.removeTaggedPortForwards(vmID)
	delete(nm.portForwards, vmID)

	// Remove MASQUERADE rule
	rule := []string{"-s", internalIP, "-j", "MASQUERADE", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s", vmID)}
	if err := nm.ipt.Delete(NATTable, PostroutingChain, rule...); err != nil {
		// Ignore error if rule doesn't exist
		if !strings.Contains(err.Error(), "No chain/target/match by that name") {
			return fmt.Errorf("failed to delete MASQUERADE rule: %w", err)
		}
	}

	// Remove FORWARD rules
	forwardRule := []string{"-s", internalIP, "-j", "ACCEPT", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-out", vmID)}
	if err := nm.ipt.Delete(FilterTable, ForwardChain, forwardRule...); err != nil {
		if !strings.Contains(err.Error(), "No chain/target/match by that name") {
			return fmt.Errorf("failed to delete FORWARD rule: %w", err)
		}
	}

	forwardRuleIn := []string{"-d", internalIP, "-j", "ACCEPT", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-in", vmID)}
	if err := nm.ipt.Delete(FilterTable, ForwardChain, forwardRuleIn...); err != nil {
		if !strings.Contains(err.Error(), "No chain/target/match by that name") {
			return fmt.Errorf("failed to delete FORWARD rule: %w", err)
		}
	}

	// Remove all port forwards for this VM
	if forwards, exists := nm.portForwards[vmID]; exists {
		for externalPort := range forwards {
			_ = nm.removePortForwardInternal(vmID, externalPort)
		}
		delete(nm.portForwards, vmID)
	}

	return nil
}

// PruneAddrs removes the masquerade and FORWARD accepts a VM holds for any
// address but keep, and duplicates of those for keep: what an address change
// or release leaves behind.
func (nm *NATManager) PruneAddrs(vmID, keep string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	tag := "maburvm-vm-" + vmID
	for _, c := range []struct {
		table, chain, comment, flag string
	}{
		{NATTable, PostroutingChain, tag, "-s"},
		{FilterTable, ForwardChain, tag + "-out", "-s"},
		{FilterTable, ForwardChain, tag + "-in", "-d"},
	} {
		lines, err := nm.ipt.List(c.table, c.chain)
		if err != nil {
			log.Printf("[NATManager] WARNING: listing %s to prune VM %s: %v", c.chain, vmID, err)
			continue
		}
		seen := false
		for _, l := range lines {
			if netrules.CountComment([]string{l}, c.comment) == 0 {
				continue
			}
			if netrules.LineAddr(l, c.flag) == keep && !seen {
				seen = true
				continue
			}
			if spec := netrules.LineSpec(l); spec != nil {
				if err := nm.ipt.Delete(c.table, c.chain, spec...); err != nil {
					log.Printf("[NATManager] WARNING: could not remove %q: %v", l, err)
				}
			}
		}
	}
}

// PortForwardSpec is one forward a VM should have.
type PortForwardSpec struct {
	External, Internal int
	Protocol           string // tcp or udp; anything else is tcp
	SourceCIDR         string
}

// SetPortForwards makes the VM's forwards exactly want: the missing ones are
// added before the ones no longer wanted (and duplicates) are removed, so a
// forward that stays never stops answering. Without a publish address nothing
// is removed either, so a passing detection failure costs no forwards.
func (nm *NATManager) SetPortForwards(vmID, internalIP string, want []PortForwardSpec) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	lines, err := nm.ipt.List(NATTable, MaburVMChain)
	if err != nil {
		return fmt.Errorf("failed to list port forwards: %w", err)
	}
	tag := fmt.Sprintf("maburvm-vm-%s-port-", vmID)
	installed := map[string]bool{}
	for _, l := range lines {
		if strings.Contains(l, tag) {
			installed[netrules.PortForwardKey(netrules.LineSpec(l))] = true
		}
	}

	var errs []string
	wanted := map[string]bool{}
	tracked := map[int]portForwardEntry{}
	if len(want) > 0 {
		if err := nm.ensurePublishAddr(); err != nil {
			return err
		}
	}
	for _, pf := range want {
		if pf.External < 1 || pf.External > 65535 || pf.Internal < 1 || pf.Internal > 65535 {
			errs = append(errs, fmt.Sprintf("invalid port forward %d->%d", pf.External, pf.Internal))
			continue
		}
		proto := pf.Protocol
		if proto != "udp" {
			proto = "tcp"
		}
		spec := netrules.PortForwardRule(nm.publishAddr, vmID, pf.External, internalIP, pf.Internal, proto, pf.SourceCIDR)
		key := netrules.PortForwardKey(spec)
		wanted[key] = true
		tracked[pf.External] = portForwardEntry{internalIP: internalIP, internalPort: pf.Internal, protocol: proto, sourceCIDR: pf.SourceCIDR}
		if installed[key] {
			continue
		}
		if err := nm.ipt.Append(NATTable, MaburVMChain, spec...); err != nil {
			errs = append(errs, fmt.Sprintf("port forward %d->%d: %v", pf.External, pf.Internal, err))
		}
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if !strings.Contains(l, tag) {
			continue
		}
		key := netrules.PortForwardKey(netrules.LineSpec(l))
		if wanted[key] && !seen[key] {
			seen[key] = true
			continue
		}
		if err := nm.ipt.Delete(NATTable, MaburVMChain, netrules.LineSpec(l)...); err != nil {
			errs = append(errs, fmt.Sprintf("removing %q: %v", l, err))
		}
	}
	if len(tracked) > 0 {
		nm.portForwards[vmID] = tracked
	} else {
		delete(nm.portForwards, vmID)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Lists returns nat POSTROUTING, filter FORWARD and the port-forward chain as
// they are now, and whether a publish address is known, for the drift check.
func (nm *NATManager) Lists() (postrouting, forward, portForwards []string, publishKnown bool) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	postrouting, _ = nm.ipt.List(NATTable, PostroutingChain)
	forward, _ = nm.ipt.List(FilterTable, ForwardChain)
	portForwards, _ = nm.ipt.List(NATTable, MaburVMChain)
	return postrouting, forward, portForwards, nm.publishAddr != ""
}

// EnsureChain puts the port-forward chain and its PREROUTING jump back if
// something outside the agent removed them, and says whether it had to.
func (nm *NATManager) EnsureChain() (bool, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	had, err := nm.ipt.ChainExists(NATTable, MaburVMChain)
	if err != nil {
		return false, err
	}
	jumped := false
	if had {
		if jumped, err = nm.ipt.Exists(NATTable, PreroutingChain, "-j", MaburVMChain); err != nil {
			return false, err
		}
	}
	if had && jumped {
		return false, nil
	}
	if !had {
		if err := nm.ipt.NewChain(NATTable, MaburVMChain); err != nil {
			return false, fmt.Errorf("failed to create chain %s: %w", MaburVMChain, err)
		}
	}
	if err := nm.ipt.Insert(NATTable, PreroutingChain, 1, "-j", MaburVMChain); err != nil {
		return true, fmt.Errorf("failed to add PREROUTING jump: %w", err)
	}
	return true, nil
}

// ClearPortForwards removes every port forward of a VM, whatever the agent
// remembers of it: the rules are found in the chain, so forwards an earlier
// agent process added (in any rule form) go too.
func (nm *NATManager) ClearPortForwards(vmID string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.removeTaggedPortForwards(vmID)
	delete(nm.portForwards, vmID)
}

// removeTaggedPortForwards deletes every DNAT rule carrying this VM's
// port-forward comment. Best-effort; assumes the lock is held.
func (nm *NATManager) removeTaggedPortForwards(vmID string) {
	nm.removeTagged(fmt.Sprintf("maburvm-vm-%s-port-", vmID))
}

// removeTagged deletes every rule of the chain whose line contains tag.
// Best-effort; assumes the lock is held.
func (nm *NATManager) removeTagged(tag string) {
	rules, err := nm.ipt.List(NATTable, MaburVMChain)
	if err != nil {
		log.Printf("[NATManager] WARNING: could not list port forwards for %s: %v", tag, err)
		return
	}
	for _, rule := range rules {
		if !strings.Contains(rule, tag) {
			continue
		}
		// "-A CHAIN <spec>": the spec has no quoted spaces, the tag is one word.
		parts := strings.SplitN(rule, " ", 3)
		if len(parts) < 3 {
			continue
		}
		if err := nm.ipt.Delete(NATTable, MaburVMChain, strings.Fields(parts[2])...); err != nil {
			log.Printf("[NATManager] WARNING: could not remove port forward %s: %v", tag, err)
		}
	}
}

// AddPortForward adds a port forwarding rule (DNAT)
// externalPort: port on the host that will be forwarded
// internalIP: IP address of the VM
// internalPort: port on the VM to forward to
func (nm *NATManager) AddPortForward(vmID string, externalPort int, internalIP string, internalPort int, protocol, sourceCIDR string) error {
	if externalPort < 1 || externalPort > 65535 {
		return fmt.Errorf("invalid external port: %d", externalPort)
	}
	if internalPort < 1 || internalPort > 65535 {
		return fmt.Errorf("invalid internal port: %d", internalPort)
	}
	if protocol != "udp" {
		protocol = "tcp"
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := nm.ensurePublishAddr(); err != nil {
		return err
	}

	// Build DNAT rule. When a source CIDR is set (and not "any"), restrict with
	// -s so a "restrict to X" forward is actually enforced, not open to everyone.
	rule := netrules.PortForwardRule(nm.publishAddr, vmID, externalPort, internalIP, internalPort, protocol, sourceCIDR)

	exists, err := nm.ipt.Exists(NATTable, MaburVMChain, rule...)
	if err != nil {
		return fmt.Errorf("failed to check port forward rule: %w", err)
	}

	if !exists {
		if err := nm.ipt.Append(NATTable, MaburVMChain, rule...); err != nil {
			return fmt.Errorf("failed to add port forward rule: %w", err)
		}
	}

	// Track the port forward
	if nm.portForwards[vmID] == nil {
		nm.portForwards[vmID] = make(map[int]portForwardEntry)
	}
	nm.portForwards[vmID][externalPort] = portForwardEntry{
		internalIP:   internalIP,
		internalPort: internalPort,
		protocol:     protocol,
		sourceCIDR:   sourceCIDR,
	}

	return nil
}

// RemovePortForward removes a port forwarding rule
func (nm *NATManager) RemovePortForward(vmID string, externalPort int) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	return nm.removePortForwardInternal(vmID, externalPort)
}

// removePortForwardInternal is the internal version that assumes lock is held
func (nm *NATManager) removePortForwardInternal(vmID string, externalPort int) error {
	// Get the stored entry
	entry, exists := nm.portForwards[vmID][externalPort]
	if !exists {
		// Not tracked (added before the agent last restarted): find it by its tag.
		nm.removeTagged(fmt.Sprintf("maburvm-vm-%s-port-%d ", vmID, externalPort))
		return nil
	}

	proto := entry.protocol
	if proto == "" {
		proto = "tcp"
	}
	// Build and delete the rule (must match the add rule exactly, incl. -s).
	rule := netrules.PortForwardRule(nm.publishAddr, vmID, externalPort, entry.internalIP, entry.internalPort, proto, entry.sourceCIDR)

	if err := nm.ipt.Delete(NATTable, MaburVMChain, rule...); err != nil {
		if !strings.Contains(err.Error(), "No chain/target/match by that name") {
			return fmt.Errorf("failed to delete port forward rule: %w", err)
		}
	}

	// Remove from tracking
	delete(nm.portForwards[vmID], externalPort)
	if len(nm.portForwards[vmID]) == 0 {
		delete(nm.portForwards, vmID)
	}

	return nil
}

// GetPortForwards returns all port forwards for a VM
func (nm *NATManager) GetPortForwards(vmID string) map[int]portForwardEntry {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	result := make(map[int]portForwardEntry)
	if forwards, exists := nm.portForwards[vmID]; exists {
		for port, entry := range forwards {
			result[port] = entry
		}
	}
	return result
}

// CleanupVM removes all NAT and port forwarding rules for a VM
// This should be called when a VM is deleted
func (nm *NATManager) CleanupVM(vmID string, internalIP string) error {
	return nm.RemoveNAT(vmID, internalIP)
}
