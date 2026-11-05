package network

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"
)

const (
	// MaburVMAntiSpoofChain is the iptables chain earlier versions kept their
	// anti-spoof rules in. Nothing is added to it any more; a VM's old rules
	// there are removed when its rules are next written.
	MaburVMAntiSpoofChain = "MABURVM-ANTISPOOF"
)

// AntiSpoofManager handles anti-IP hijacking and ARP spoofing prevention.
// Layer 1: libvirt nwfilter (applied via domain XML in vm.go).
// Layer 2: a per-VM ebtables chain pinning each restricted NIC's ARP and IPv4
// to its addresses, and its ARP to its MAC.
//
// Earlier versions did the IPv4 part in iptables with a single address per VM,
// which cut off a VM with two addresses: whichever the panel sent last won.
type AntiSpoofManager struct {
	ipt  *iptables.IPTables
	ipt6 *iptables.IPTables
	mu   sync.RWMutex
	// vmRules tracks active rules per VM: map[vmID]*spoofRules
	vmRules map[string]*spoofRules
}

// SpoofAddr is an address a VM may use, with the bridge of the pool it comes
// from ("" when unknown).
type SpoofAddr struct {
	IP     string
	Bridge string
}

// NIC is one of a VM's interfaces as it is now. Tap and Bridge are empty while
// the VM is not running.
type NIC struct {
	MAC    string
	Tap    string
	Bridge string
}

// spoofRules holds the rules applied for a specific VM
type spoofRules struct {
	VMID  string
	Addrs []SpoofAddr
	// Key identifies the taps (and their bridges) the rules were last written
	// for; RebindInterfaces rewrites them when it changes.
	Key string
}

// spoofNIC is a NIC with the addresses it may use.
type spoofNIC struct {
	NIC
	IPs []string
}

// NewAntiSpoofManager creates a new AntiSpoofManager instance
func NewAntiSpoofManager() (*AntiSpoofManager, error) {
	ipt, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4))
	if err != nil {
		return nil, fmt.Errorf("failed to create iptables client: %w", err)
	}

	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6))
	if err != nil {
		// IPv6 is optional, log but don't fail
		ipt6 = nil
	}

	return &AntiSpoofManager{
		ipt:     ipt,
		ipt6:    ipt6,
		vmRules: make(map[string]*spoofRules),
	}, nil
}

// nicKey identifies a VM's current taps and bridges.
func nicKey(nics []NIC) string {
	var b strings.Builder
	for _, n := range nics {
		if n.Tap != "" {
			b.WriteString(n.Tap + "@" + n.Bridge + " ")
		}
	}
	return b.String()
}

// assignAddrs gives each address to the NIC on its pool's bridge, and an address
// with no such NIC (bridge unknown, or not one the VM is on) to the first NIC.
// NICs left without an address are not restricted: the panel doesn't know what
// they carry (a private VLAN, say), and pinning them to the VM's public
// addresses cut such VMs off. NICs without a tap (VM not running) are skipped.
func assignAddrs(addrs []SpoofAddr, nics []NIC) []spoofNIC {
	if len(nics) == 0 {
		return nil
	}
	all := make([]spoofNIC, len(nics))
	for i, n := range nics {
		all[i].NIC = n
	}
	for _, a := range addrs {
		i := 0
		for j, n := range nics {
			if a.Bridge != "" && n.Bridge == a.Bridge {
				i = j
				break
			}
		}
		all[i].IPs = append(all[i].IPs, a.IP)
	}
	var out []spoofNIC
	for _, n := range all {
		if n.Tap != "" && len(n.IPs) > 0 {
			out = append(out, n)
		}
	}
	return out
}

// ebRuleCommands builds the ebtables commands creating a VM's chain. Within
// the chain RETURN only ends this VM's checks; FORWARD goes on to the next
// VM's chain, so traffic between two VMs is checked on both ends.
func ebRuleCommands(vmID string, nics []spoofNIC) [][]string {
	chain := ebChain(vmID)
	// A new ebtables chain defaults to ACCEPT, which would end FORWARD
	// processing and skip every VM chain after this one.
	cmds := [][]string{{"-N", chain}, {"-P", chain, "RETURN"}}
	add := func(rule ...string) { cmds = append(cmds, append([]string{"-A", chain}, rule...)) }
	allow := func(proto, dir, tap, match string, ips []string) {
		for _, ip := range ips {
			add("-p", proto, dir, tap, match, ip, "-j", "RETURN")
		}
		add("-p", proto, dir, tap, "-j", "DROP")
	}
	// ARP is pinned to each NIC's own addresses, but IPv4 may carry any of the
	// VM's addresses: a multi-homed guest answers on one address out of the
	// NIC its route to the client points at.
	var all []string
	for _, n := range nics {
		for _, ip := range n.IPs {
			all = appendNew(all, ip)
		}
	}
	for _, n := range nics {
		add("-p", "ARP", "-i", n.Tap, "--arp-mac-src", "!", n.MAC, "-j", "DROP")
		allow("ARP", "-i", n.Tap, "--arp-ip-src", n.IPs)
		allow("ARP", "-o", n.Tap, "--arp-ip-dst", n.IPs)
		allow("IPv4", "-i", n.Tap, "--ip-src", all)
		allow("IPv4", "-o", n.Tap, "--ip-dst", all)
	}
	return append(cmds, []string{"-A", "FORWARD", "-j", chain})
}

// ApplyAntiSpoofRules pins a VM's NICs to addrs. A VM that isn't running only
// has its rules recorded; RebindInterfaces applies them once it has taps.
func (asm *AntiSpoofManager) ApplyAntiSpoofRules(vmID string, addrs []SpoofAddr, nics []NIC) error {
	if vmID == "" {
		return fmt.Errorf("VM ID cannot be empty")
	}
	if len(addrs) == 0 {
		return fmt.Errorf("at least one IP address must be provided")
	}

	asm.mu.Lock()
	defer asm.mu.Unlock()

	if err := asm.removeRulesInternal(vmID); err != nil {
		return fmt.Errorf("failed to remove existing rules: %w", err)
	}
	r := &spoofRules{VMID: vmID, Addrs: addrs}
	asm.vmRules[vmID] = r
	return asm.write(r, nics)
}

// write puts r on the host for the VM's current NICs (assumes lock is held).
func (asm *AntiSpoofManager) write(r *spoofRules, nics []NIC) error {
	r.Key = nicKey(nics)
	restricted := assignAddrs(r.Addrs, nics)
	if len(restricted) == 0 {
		return nil
	}
	if _, err := exec.LookPath("ebtables"); err != nil {
		return nil // ebtables not installed; nwfilter is all there is
	}
	for _, c := range ebRuleCommands(r.VMID, restricted) {
		if err := ebtables(c...); err != nil {
			_ = asm.removeFromHost(r.VMID)
			r.Key = "" // retried by RebindInterfaces
			return err
		}
	}
	return nil
}

// ebChain names the per-VM ebtables chain holding a VM's rules. ebtables
// rules carry no comments, so a chain named after the VM is the only way to
// find them again once the agent's memory of them is gone (restart, hand
// edits). Chain names are capped at 31 chars, hence the truncated id.
func ebChain(vmID string) string {
	id := strings.ReplaceAll(vmID, "-", "")
	if len(id) > 20 {
		id = id[:20]
	}
	return "MAS-" + id
}

func ebtables(args ...string) error {
	if output, err := exec.Command("ebtables", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ebtables %s: %v, output: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// removeEBTablesRules removes a VM's ebtables chain and the FORWARD jump to it.
// It needs only the VM id, so it also works for rules the agent no longer tracks.
func removeEBTablesRules(vmID string) error {
	if _, err := exec.LookPath("ebtables"); err != nil {
		return nil // ebtables not installed, nothing to remove
	}
	chain := ebChain(vmID)
	if ebtables("-L", chain) != nil {
		return nil // no chain, nothing to remove
	}
	for i := 0; i < 10 && ebtables("-D", "FORWARD", "-j", chain) == nil; i++ {
	}
	if err := ebtables("-F", chain); err != nil {
		return err
	}
	return ebtables("-X", chain)
}

// RemoveAntiSpoofRules removes all anti-spoofing rules for a VM
// This should be called when a VM is stopped or deleted
func (asm *AntiSpoofManager) RemoveAntiSpoofRules(vmID string) error {
	asm.mu.Lock()
	defer asm.mu.Unlock()

	return asm.removeRulesInternal(vmID)
}

// removeRulesInternal removes rules for a VM (assumes lock is held)
func (asm *AntiSpoofManager) removeRulesInternal(vmID string) error {
	delete(asm.vmRules, vmID)
	return asm.removeFromHost(vmID)
}

// RebindInterfaces rewrites a VM's rules when its taps changed. A VM restart
// gives it new vnetX names, and rules left on the old ones protect nothing
// while dropping all traffic of whichever VM gets those names next. A VM with
// no taps (stopped) has its rules lifted until it runs again. Lookup errors
// leave the VM alone.
func (asm *AntiSpoofManager) RebindInterfaces(lookup func(vmID string) ([]NIC, error)) {
	asm.mu.Lock()
	defer asm.mu.Unlock()

	for vmID, r := range asm.vmRules {
		nics, err := lookup(vmID)
		if err != nil || nicKey(nics) == r.Key {
			continue
		}
		if err := asm.removeFromHost(vmID); err != nil {
			log.Printf("[AntiSpoof] VM %s: could not remove rules for %q: %v", vmID, r.Key, err)
			continue
		}
		if err := asm.write(r, nics); err != nil {
			log.Printf("[AntiSpoof] VM %s: could not rebind rules: %v", vmID, err)
		} else if r.Key == "" {
			log.Printf("[AntiSpoof] VM %s has no tap device, rules lifted until it runs", vmID)
		} else {
			log.Printf("[AntiSpoof] VM %s rules rebound to %s", vmID, r.Key)
		}
	}
}

// AdoptExisting rebuilds the tracked state from the rules already on the host,
// so a restarted agent keeps following VMs onto new taps. vmIDs are the VMs
// worth checking (the running ones); VMs with rules in the old iptables layout
// are checked too and moved to the current one. Rules of VMs libvirt no longer
// knows are removed.
func (asm *AntiSpoofManager) AdoptExisting(vmIDs []string, nicsOf func(vmID string) ([]NIC, error)) {
	asm.mu.Lock()
	defer asm.mu.Unlock()

	legacy := map[string][]string{}
	for _, ipt := range []*iptables.IPTables{asm.ipt, asm.ipt6} {
		if ipt == nil {
			continue
		}
		lines, err := ipt.List(FilterTable, MaburVMAntiSpoofChain)
		if err != nil {
			continue
		}
		for _, line := range lines {
			if vmID, addr := parseAntiSpoofRule(line); vmID != "" && addr != "" && ipt == asm.ipt {
				legacy[vmID] = appendNew(legacy[vmID], strings.TrimSuffix(addr, "/32"))
			}
		}
	}

	// Even older layout: ARP DROPs straight in FORWARD.
	if _, err := exec.LookPath("ebtables"); err == nil {
		if out, err := exec.Command("ebtables", "-L", "FORWARD").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "-p ARP ") && strings.HasSuffix(line, "-j DROP") {
					_ = ebtables(append([]string{"-D", "FORWARD"}, strings.Fields(line)...)...)
				}
			}
		}
	}

	candidates := map[string]bool{}
	for _, id := range vmIDs {
		candidates[id] = true
	}
	for id := range legacy {
		candidates[id] = true
	}
	for vmID := range candidates {
		nics, err := nicsOf(vmID)
		if err != nil {
			if len(legacy[vmID]) > 0 {
				_ = asm.removeFromHost(vmID)
				log.Printf("[AntiSpoof] VM %s: dropped rules, no NICs from libvirt: %v", vmID, err)
			}
			continue
		}
		addrs := chainAddrs(vmID, nics)
		if len(addrs) == 0 {
			for _, ip := range legacy[vmID] {
				addrs = append(addrs, SpoofAddr{IP: ip})
			}
		}
		if len(addrs) == 0 {
			continue // no anti-spoof on this VM
		}
		_ = asm.removeFromHost(vmID)
		r := &spoofRules{VMID: vmID, Addrs: addrs}
		if err := asm.write(r, nics); err != nil {
			log.Printf("[AntiSpoof] VM %s: could not re-apply rules: %v", vmID, err)
		}
		asm.vmRules[vmID] = r
	}
	log.Printf("[AntiSpoof] adopted rules of %d VMs", len(asm.vmRules))
}

// chainAddrs reads the addresses a VM's ebtables chain allows, each with the
// bridge of the tap it was allowed on.
func chainAddrs(vmID string, nics []NIC) []SpoofAddr {
	out, err := exec.Command("ebtables", "-L", ebChain(vmID)).Output()
	if err != nil {
		return nil
	}
	bridgeOf := map[string]string{}
	for _, n := range nics {
		bridgeOf[n.Tap] = n.Bridge
	}
	var addrs []SpoofAddr
	for _, line := range strings.Split(string(out), "\n") {
		if tap, ip := parseChainRule(line); ip != "" {
			addrs = append(addrs, SpoofAddr{IP: ip, Bridge: bridgeOf[tap]})
		}
	}
	return addrs
}

// parseChainRule extracts the tap and address of an outbound ARP allow rule
// written by ebRuleCommands; ARP rules carry only the NIC's own addresses.
func parseChainRule(line string) (tap, ip string) {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "-i":
			tap = f[i+1]
		case "--arp-ip-src":
			ip = f[i+1]
		}
	}
	if ip == "" || tap == "" {
		return "", ""
	}
	return tap, ip
}

func appendNew(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// parseAntiSpoofRule extracts the VM id and the protected address from a rule
// in the old iptables layout.
func parseAntiSpoofRule(line string) (vmID, addr string) {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "-s", "-d":
			addr = f[i+1] // iptables prints a host as /32
		case "--comment":
			c := strings.Trim(f[i+1], `"`)
			if id, ok := strings.CutPrefix(c, "maburvm-antispoof-"); ok && len(id) > 36 {
				vmID = id[:36]
			}
		}
	}
	return vmID, addr
}

// removeFromHost removes a VM's iptables (old layout) and ebtables rules by
// comment and chain name, without consulting the tracked state.
func (asm *AntiSpoofManager) removeFromHost(vmID string) error {
	var errs []string

	if err := asm.cleanupByComment(vmID, asm.ipt); err != nil {
		errs = append(errs, fmt.Sprintf("iptables: %v", err))
	}
	if asm.ipt6 != nil {
		if err := asm.cleanupByComment(vmID, asm.ipt6); err != nil {
			errs = append(errs, fmt.Sprintf("ip6tables: %v", err))
		}
	}
	if err := removeEBTablesRules(vmID); err != nil {
		errs = append(errs, fmt.Sprintf("ebtables: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// cleanupByComment removes iptables rules matching a VM ID comment
func (asm *AntiSpoofManager) cleanupByComment(vmID string, ipt *iptables.IPTables) error {
	exists, err := ipt.ChainExists(FilterTable, MaburVMAntiSpoofChain)
	if err != nil {
		return fmt.Errorf("failed to check chain: %w", err)
	}
	if !exists {
		return nil // nothing of ours can be in a chain that was never created
	}
	rules, err := ipt.List(FilterTable, MaburVMAntiSpoofChain)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}

	prefix := fmt.Sprintf("maburvm-antispoof-%s", vmID)
	for _, rule := range rules {
		if strings.Contains(rule, prefix) {
			// Parse the rule specification (skip "-A MABURVM-ANTISPOOF ")
			parts := strings.SplitN(rule, " ", 3)
			if len(parts) >= 3 {
				ruleSpec := strings.Fields(parts[2])
				if err := ipt.Delete(FilterTable, MaburVMAntiSpoofChain, ruleSpec...); err != nil {
					// Ignore errors for non-existent rules
					if !strings.Contains(err.Error(), "No chain/target/match by that name") {
						return fmt.Errorf("failed to delete rule: %w", err)
					}
				}
			}
		}
	}

	return nil
}

// CleanupVM removes all anti-spoofing rules for a VM
// This is an alias for RemoveAntiSpoofRules for consistency with other managers
func (asm *AntiSpoofManager) CleanupVM(vmID string) error {
	return asm.RemoveAntiSpoofRules(vmID)
}
