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
	// MaburVMAntiSpoofChain is the custom chain for MaburVM anti-spoofing rules
	MaburVMAntiSpoofChain = "MABURVM-ANTISPOOF"
)

// AntiSpoofManager handles anti-IP hijacking and ARP spoofing prevention
// Implements defense-in-depth with 3 layers:
// Layer 1: Libvirt nwfilter (applied via domain XML in vm.go)
// Layer 2: iptables FORWARD chain rules (L3 filtering)
// Layer 3: ebtables ARP filtering (L2 filtering)
type AntiSpoofManager struct {
	ipt  *iptables.IPTables
	ipt6 *iptables.IPTables
	mu   sync.RWMutex
	// vmRules tracks active rules per VM: map[vmID]*spoofRules
	vmRules map[string]*spoofRules
}

// spoofRules holds the rules applied for a specific VM
type spoofRules struct {
	VMID      string
	IP        string
	IPv6      string
	MAC       string
	Interface string // vnetX interface name
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

	asm := &AntiSpoofManager{
		ipt:     ipt,
		ipt6:    ipt6,
		vmRules: make(map[string]*spoofRules),
	}

	// Ensure our custom chain exists
	if err := asm.ensureChain(); err != nil {
		return nil, err
	}

	return asm, nil
}

// ensureChain creates the custom MaburVM anti-spoof chain if it doesn't exist
func (asm *AntiSpoofManager) ensureChain() error {
	// IPv4 chain
	chains, err := asm.ipt.ListChains(FilterTable)
	if err != nil {
		return fmt.Errorf("failed to list chains: %w", err)
	}

	chainExists := false
	for _, chain := range chains {
		if chain == MaburVMAntiSpoofChain {
			chainExists = true
			break
		}
	}

	if !chainExists {
		// Create the chain
		if err := asm.ipt.NewChain(FilterTable, MaburVMAntiSpoofChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", MaburVMAntiSpoofChain, err)
		}

		// Jump to our chain from FORWARD for bridged traffic
		exists, err := asm.ipt.Exists(FilterTable, ForwardChain, "-j", MaburVMAntiSpoofChain)
		if err != nil {
			return fmt.Errorf("failed to check FORWARD jump: %w", err)
		}
		if !exists {
			// Insert at position 1 to ensure it's evaluated before other rules
			if err := asm.ipt.Insert(FilterTable, ForwardChain, 1, "-j", MaburVMAntiSpoofChain); err != nil {
				return fmt.Errorf("failed to add FORWARD jump: %w", err)
			}
		}
	}

	return nil
}

// ApplyAntiSpoofRules applies anti-spoofing rules for a VM. An empty
// vnetInterface (VM not running) only records the rules; RebindInterfaces
// applies them once the VM has a tap.
func (asm *AntiSpoofManager) ApplyAntiSpoofRules(vmID, ip, ipv6, mac, vnetInterface string) error {
	if vmID == "" {
		return fmt.Errorf("VM ID cannot be empty")
	}
	if ip == "" && ipv6 == "" {
		return fmt.Errorf("at least one IP address (IPv4 or IPv6) must be provided")
	}
	if mac == "" {
		return fmt.Errorf("MAC address cannot be empty")
	}

	asm.mu.Lock()
	defer asm.mu.Unlock()

	// Remove existing rules if any
	if err := asm.removeRulesInternal(vmID); err != nil {
		return fmt.Errorf("failed to remove existing rules: %w", err)
	}

	rules := &spoofRules{
		VMID:      vmID,
		IP:        ip,
		IPv6:      ipv6,
		MAC:       mac,
		Interface: vnetInterface,
	}
	if vnetInterface == "" {
		asm.vmRules[vmID] = rules
		return nil
	}

	// Layer 2: iptables FORWARD chain rules (L3 filtering)
	if err := asm.applyIPTablesRules(rules); err != nil {
		_ = asm.removeRulesInternal(vmID)
		return fmt.Errorf("failed to apply iptables rules: %w", err)
	}

	// Layer 3: ebtables ARP filtering (L2 filtering)
	if err := asm.applyEBTablesRules(rules); err != nil {
		_ = asm.removeRulesInternal(vmID)
		return fmt.Errorf("failed to apply ebtables rules: %w", err)
	}

	asm.vmRules[vmID] = rules
	return nil
}

// applyIPTablesRules applies iptables FORWARD chain rules for L3 filtering
// Uses physdev module to match traffic on specific vnet interfaces
func (asm *AntiSpoofManager) applyIPTablesRules(rules *spoofRules) error {
	// IPv4 rules
	if rules.IP != "" {
		// Drop outbound packets NOT from the VM's assigned IP
		// iptables -I MABURVM-ANTISPOOF -m physdev --physdev-is-bridged --physdev-in vnetX -s ! $VM_IP -j DROP
		outRule := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-in", rules.Interface,
			"!", "-s", rules.IP,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-out", rules.VMID),
		}
		if err := asm.ipt.Insert(FilterTable, MaburVMAntiSpoofChain, 1, outRule...); err != nil {
			return fmt.Errorf("failed to add outbound IPv4 rule: %w", err)
		}

		// Drop inbound packets NOT destined for the VM's assigned IP
		// iptables -I MABURVM-ANTISPOOF -m physdev --physdev-is-bridged --physdev-out vnetX -d ! $VM_IP -j DROP
		inRule := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-out", rules.Interface,
			"!", "-d", rules.IP,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-in", rules.VMID),
		}
		if err := asm.ipt.Insert(FilterTable, MaburVMAntiSpoofChain, 1, inRule...); err != nil {
			return fmt.Errorf("failed to add inbound IPv4 rule: %w", err)
		}
	}

	// IPv6 rules
	if rules.IPv6 != "" && asm.ipt6 != nil {
		// Drop outbound packets NOT from the VM's assigned IPv6
		outRule6 := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-in", rules.Interface,
			"!", "-s", rules.IPv6,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-out6", rules.VMID),
		}
		if err := asm.ipt6.Insert(FilterTable, MaburVMAntiSpoofChain, 1, outRule6...); err != nil {
			return fmt.Errorf("failed to add outbound IPv6 rule: %w", err)
		}

		// Drop inbound packets NOT destined for the VM's assigned IPv6
		inRule6 := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-out", rules.Interface,
			"!", "-d", rules.IPv6,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-in6", rules.VMID),
		}
		if err := asm.ipt6.Insert(FilterTable, MaburVMAntiSpoofChain, 1, inRule6...); err != nil {
			return fmt.Errorf("failed to add inbound IPv6 rule: %w", err)
		}
	}

	return nil
}

// ebChain names the per-VM ebtables chain holding a VM's ARP rules. ebtables
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

// applyEBTablesRules applies ebtables rules for ARP spoofing prevention
// ebtables filters at Layer 2 to prevent ARP poisoning attacks
func (asm *AntiSpoofManager) applyEBTablesRules(rules *spoofRules) error {
	// Check if ebtables is available
	if _, err := exec.LookPath("ebtables"); err != nil {
		// ebtables not installed, skip ARP filtering
		// This is not critical as iptables rules still provide L3 protection
		return nil
	}

	chain := ebChain(rules.VMID)
	// A new ebtables chain defaults to ACCEPT, which would end FORWARD
	// processing and skip every VM chain after this one.
	cmds := [][]string{{"-N", chain}, {"-P", chain, "RETURN"}}
	if rules.IP != "" {
		cmds = append(cmds,
			// Drop ARP with spoofed source IP from this VM's tap
			[]string{"-A", chain, "-p", "ARP", "--arp-ip-src", "!", rules.IP, "-i", rules.Interface, "-j", "DROP"},
			// Drop ARP destined for wrong IP going to this VM
			[]string{"-A", chain, "-p", "ARP", "--arp-ip-dst", "!", rules.IP, "-o", rules.Interface, "-j", "DROP"})
	}
	cmds = append(cmds,
		// Drop ARP with spoofed source MAC from this VM's tap
		[]string{"-A", chain, "-p", "ARP", "--arp-mac-src", "!", rules.MAC, "-i", rules.Interface, "-j", "DROP"},
		[]string{"-A", "FORWARD", "-j", chain})
	for _, c := range cmds {
		if err := ebtables(c...); err != nil {
			return err
		}
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

// RebindInterfaces re-applies a VM's rules when its tap device changed. A VM
// restart gives it a new vnetX, and rules left on the old name protect nothing
// while dropping all ARP of whichever VM gets that name next. lookup returns ""
// for a VM with no tap (stopped): its rules are lifted until it runs again.
// Lookup errors leave the VM alone.
func (asm *AntiSpoofManager) RebindInterfaces(lookup func(vmID string) (string, error)) {
	asm.mu.Lock()
	defer asm.mu.Unlock()

	for vmID, rules := range asm.vmRules {
		iface, err := lookup(vmID)
		if err != nil || iface == rules.Interface {
			continue
		}
		if err := asm.removeFromHost(vmID); err != nil {
			log.Printf("[AntiSpoof] VM %s: could not remove rules for %q: %v", vmID, rules.Interface, err)
			continue
		}
		rules.Interface = iface
		if iface == "" {
			log.Printf("[AntiSpoof] VM %s has no tap device, rules lifted until it runs", vmID)
			continue
		}
		err = asm.applyIPTablesRules(rules)
		if err == nil {
			err = asm.applyEBTablesRules(rules)
		}
		if err != nil {
			_ = asm.removeFromHost(vmID)
			rules.Interface = "" // retried on the next pass
			log.Printf("[AntiSpoof] VM %s: could not rebind rules to %s: %v", vmID, iface, err)
			continue
		}
		log.Printf("[AntiSpoof] VM %s rules rebound to %s", vmID, iface)
	}
}

// AdoptExisting rebuilds the tracked state from the anti-spoof rules already on
// the host (comment → VM id, -s/-d → address), so a restarted agent keeps
// following VMs onto new taps; nothing re-sends the config after a restart.
// Each VM is re-applied on its current tap, which also moves it off the old
// layout of ARP DROPs straight in FORWARD; any of those left are stale and
// removed. Rules of VMs libvirt no longer knows are removed too.
func (asm *AntiSpoofManager) AdoptExisting(macOf, tapOf func(vmID string) (string, error)) {
	asm.mu.Lock()
	defer asm.mu.Unlock()

	found := map[string]*spoofRules{}
	for _, ipt := range []*iptables.IPTables{asm.ipt, asm.ipt6} {
		if ipt == nil {
			continue
		}
		lines, err := ipt.List(FilterTable, MaburVMAntiSpoofChain)
		if err != nil {
			continue
		}
		for _, line := range lines {
			vmID, addr := parseAntiSpoofRule(line)
			if vmID == "" || addr == "" {
				continue
			}
			r := found[vmID]
			if r == nil {
				r = &spoofRules{VMID: vmID}
				found[vmID] = r
			}
			if ipt == asm.ipt {
				r.IP = addr
			} else {
				r.IPv6 = addr
			}
		}
	}

	if _, err := exec.LookPath("ebtables"); err == nil {
		if out, err := exec.Command("ebtables", "-L", "FORWARD").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "-p ARP ") && strings.HasSuffix(line, "-j DROP") {
					_ = ebtables(append([]string{"-D", "FORWARD"}, strings.Fields(line)...)...)
				}
			}
		}
	}

	for vmID, r := range found {
		_ = asm.removeFromHost(vmID)
		mac, err := macOf(vmID)
		if err != nil || mac == "" {
			log.Printf("[AntiSpoof] VM %s: dropped rules, no MAC from libvirt: %v", vmID, err)
			continue
		}
		r.MAC = mac
		if r.Interface, err = tapOf(vmID); err != nil {
			r.Interface = ""
		}
		if r.Interface != "" {
			err = asm.applyIPTablesRules(r)
			if err == nil {
				err = asm.applyEBTablesRules(r)
			}
			if err != nil {
				_ = asm.removeFromHost(vmID)
				r.Interface = "" // retried by RebindInterfaces
				log.Printf("[AntiSpoof] VM %s: could not re-apply rules: %v", vmID, err)
			}
		}
		asm.vmRules[vmID] = r
	}
	log.Printf("[AntiSpoof] adopted rules of %d VMs", len(asm.vmRules))
}

// parseAntiSpoofRule extracts the VM id and the protected address from a rule
// written by applyIPTablesRules.
func parseAntiSpoofRule(line string) (vmID, addr string) {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "-s", "-d":
			addr = f[i+1] // iptables prints a host as /32, which re-applies the same
		case "--comment":
			c := strings.Trim(f[i+1], `"`)
			if id, ok := strings.CutPrefix(c, "maburvm-antispoof-"); ok && len(id) > 36 {
				vmID = id[:36]
			}
		}
	}
	return vmID, addr
}

// removeFromHost removes a VM's iptables and ebtables rules by comment and
// chain name, without consulting the tracked state.
func (asm *AntiSpoofManager) removeFromHost(vmID string) error {
	var errs []string

	// Scan iptables for rules with our comment
	if err := asm.cleanupByComment(vmID, asm.ipt); err != nil {
		errs = append(errs, fmt.Sprintf("iptables: %v", err))
	}

	// Scan ip6tables if available
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
		// Nothing of ours can be in a chain that was never created (the IPv6 one
		// never is). Failing here used to abort ApplyAntiSpoofRules, so after
		// every agent restart the IPv4 anti-spoof rules were silently skipped.
		return nil
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
