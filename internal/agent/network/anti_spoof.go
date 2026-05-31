package network

import (
	"fmt"
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

// ApplyAntiSpoofRules applies anti-spoofing rules for a VM
// This should be called when a VM is started
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
	if vnetInterface == "" {
		return fmt.Errorf("vnet interface cannot be empty")
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

// applyEBTablesRules applies ebtables rules for ARP spoofing prevention
// ebtables filters at Layer 2 to prevent ARP poisoning attacks
func (asm *AntiSpoofManager) applyEBTablesRules(rules *spoofRules) error {
	// Check if ebtables is available
	if _, err := exec.LookPath("ebtables"); err != nil {
		// ebtables not installed, skip ARP filtering
		// This is not critical as iptables rules still provide L3 protection
		return nil
	}

	// Drop ARP with spoofed source IP from this VM's tap
	// ebtables -A FORWARD -p ARP --arp-ip-src ! $VM_IP -i vnetX -j DROP
	if rules.IP != "" {
		cmd := exec.Command("ebtables", "-A", "FORWARD",
			"-p", "ARP",
			"--arp-ip-src", "!", rules.IP,
			"-i", rules.Interface,
			"-j", "DROP")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ebtables ARP IP src rule failed: %v, output: %s", err, string(output))
		}

		// Drop ARP destined for wrong IP going to this VM
		// ebtables -A FORWARD -p ARP --arp-ip-dst ! $VM_IP -o vnetX -j DROP
		cmd = exec.Command("ebtables", "-A", "FORWARD",
			"-p", "ARP",
			"--arp-ip-dst", "!", rules.IP,
			"-o", rules.Interface,
			"-j", "DROP")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ebtables ARP IP dst rule failed: %v, output: %s", err, string(output))
		}
	}

	// Drop ARP with spoofed source MAC from this VM's tap
	// ebtables -A FORWARD -p ARP --arp-mac-src ! $VM_MAC -i vnetX -j DROP
	cmd := exec.Command("ebtables", "-A", "FORWARD",
		"-p", "ARP",
		"--arp-mac-src", "!", rules.MAC,
		"-i", rules.Interface,
		"-j", "DROP")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ebtables ARP MAC src rule failed: %v, output: %s", err, string(output))
	}

	return nil
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
	rules, exists := asm.vmRules[vmID]
	if !exists {
		// No rules to remove, try best-effort cleanup anyway
		return asm.bestEffortCleanup(vmID)
	}

	var errs []string

	// Remove iptables rules
	if err := asm.removeIPTablesRules(rules); err != nil {
		errs = append(errs, fmt.Sprintf("iptables: %v", err))
	}

	// Remove ebtables rules
	if err := asm.removeEBTablesRules(rules); err != nil {
		errs = append(errs, fmt.Sprintf("ebtables: %v", err))
	}

	// Remove from tracking
	delete(asm.vmRules, vmID)

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}

	return nil
}

// removeIPTablesRules removes iptables rules for a VM
func (asm *AntiSpoofManager) removeIPTablesRules(rules *spoofRules) error {
	var errs []string

	// Remove IPv4 rules
	if rules.IP != "" {
		// Outbound rule
		outRule := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-in", rules.Interface,
			"!", "-s", rules.IP,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-out", rules.VMID),
		}
		if err := asm.ipt.Delete(FilterTable, MaburVMAntiSpoofChain, outRule...); err != nil {
			if !strings.Contains(err.Error(), "No chain/target/match by that name") &&
				!strings.Contains(err.Error(), "Bad rule") {
				errs = append(errs, fmt.Sprintf("IPv4 out: %v", err))
			}
		}

		// Inbound rule
		inRule := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-out", rules.Interface,
			"!", "-d", rules.IP,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-in", rules.VMID),
		}
		if err := asm.ipt.Delete(FilterTable, MaburVMAntiSpoofChain, inRule...); err != nil {
			if !strings.Contains(err.Error(), "No chain/target/match by that name") &&
				!strings.Contains(err.Error(), "Bad rule") {
				errs = append(errs, fmt.Sprintf("IPv4 in: %v", err))
			}
		}
	}

	// Remove IPv6 rules
	if rules.IPv6 != "" && asm.ipt6 != nil {
		// Outbound rule
		outRule6 := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-in", rules.Interface,
			"!", "-s", rules.IPv6,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-out6", rules.VMID),
		}
		if err := asm.ipt6.Delete(FilterTable, MaburVMAntiSpoofChain, outRule6...); err != nil {
			if !strings.Contains(err.Error(), "No chain/target/match by that name") &&
				!strings.Contains(err.Error(), "Bad rule") {
				errs = append(errs, fmt.Sprintf("IPv6 out: %v", err))
			}
		}

		// Inbound rule
		inRule6 := []string{
			"-m", "physdev",
			"--physdev-is-bridged",
			"--physdev-out", rules.Interface,
			"!", "-d", rules.IPv6,
			"-j", "DROP",
			"-m", "comment",
			"--comment", fmt.Sprintf("maburvm-antispoof-%s-in6", rules.VMID),
		}
		if err := asm.ipt6.Delete(FilterTable, MaburVMAntiSpoofChain, inRule6...); err != nil {
			if !strings.Contains(err.Error(), "No chain/target/match by that name") &&
				!strings.Contains(err.Error(), "Bad rule") {
				errs = append(errs, fmt.Sprintf("IPv6 in: %v", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	return nil
}

// removeEBTablesRules removes ebtables rules for a VM
func (asm *AntiSpoofManager) removeEBTablesRules(rules *spoofRules) error {
	// Check if ebtables is available
	if _, err := exec.LookPath("ebtables"); err != nil {
		return nil // ebtables not installed, nothing to remove
	}

	var errs []string

	// Remove ARP IP source rule
	if rules.IP != "" {
		cmd := exec.Command("ebtables", "-D", "FORWARD",
			"-p", "ARP",
			"--arp-ip-src", "!", rules.IP,
			"-i", rules.Interface,
			"-j", "DROP")
		if output, err := cmd.CombinedOutput(); err != nil {
			if !strings.Contains(string(output), "rule not found") {
				errs = append(errs, fmt.Sprintf("ARP IP src: %v", err))
			}
		}

		// Remove ARP IP destination rule
		cmd = exec.Command("ebtables", "-D", "FORWARD",
			"-p", "ARP",
			"--arp-ip-dst", "!", rules.IP,
			"-o", rules.Interface,
			"-j", "DROP")
		if output, err := cmd.CombinedOutput(); err != nil {
			if !strings.Contains(string(output), "rule not found") {
				errs = append(errs, fmt.Sprintf("ARP IP dst: %v", err))
			}
		}
	}

	// Remove ARP MAC source rule
	cmd := exec.Command("ebtables", "-D", "FORWARD",
		"-p", "ARP",
		"--arp-mac-src", "!", rules.MAC,
		"-i", rules.Interface,
		"-j", "DROP")
	if output, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(output), "rule not found") {
			errs = append(errs, fmt.Sprintf("ARP MAC src: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	return nil
}

// bestEffortCleanup attempts to remove rules by scanning iptables/ebtables for matching comments
func (asm *AntiSpoofManager) bestEffortCleanup(vmID string) error {
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

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	return nil
}

// cleanupByComment removes iptables rules matching a VM ID comment
func (asm *AntiSpoofManager) cleanupByComment(vmID string, ipt *iptables.IPTables) error {
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
