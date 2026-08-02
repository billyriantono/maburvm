package network

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"
	"github.com/maburvm/panel/internal/shared/models"
)

const (
	// MaburVMFirewallChain is the custom chain for MaburVM firewall rules
	MaburVMFirewallChain = "MABURVM-FIREWALL"
	// InputChain is the INPUT chain in filter table
	InputChain = "INPUT"
	// OutputChain is the OUTPUT chain in filter table
	OutputChain = "OUTPUT"
)

// FirewallManager handles firewall rules for VMs
type FirewallManager struct {
	ipt *iptables.IPTables
	mu  sync.RWMutex
	// rules tracks active rules per VM: map[vmID]map[ruleID][]ruleSpec
	rules map[string]map[string][]string
}

// FirewallRule represents a firewall rule for iptables
// This mirrors models.FirewallRule but with validation
type FirewallRule struct {
	ID        string
	Protocol  string // tcp, udp, icmp, all
	PortRange string // e.g., "80", "1000:2000", empty for all ports
	Action    string // allow, deny
	Direction string // inbound, outbound
	SourceIP  string // CIDR notation, e.g., "0.0.0.0/0"
	Priority  int    // 1-1000, lower = higher priority
}

// NewFirewallManager creates a new FirewallManager instance
func NewFirewallManager() (*FirewallManager, error) {
	ipt, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4))
	if err != nil {
		return nil, fmt.Errorf("failed to create iptables client: %w", err)
	}

	fm := &FirewallManager{
		ipt:   ipt,
		rules: make(map[string]map[string][]string),
	}

	// Ensure our custom chain exists
	if err := fm.ensureChain(); err != nil {
		return nil, err
	}

	return fm, nil
}

// ensureChain creates the custom MaburVM firewall chain if it doesn't exist
func (fm *FirewallManager) ensureChain() error {
	chains, err := fm.ipt.ListChains(FilterTable)
	if err != nil {
		return fmt.Errorf("failed to list chains: %w", err)
	}

	chainExists := false
	for _, chain := range chains {
		if chain == MaburVMFirewallChain {
			chainExists = true
			break
		}
	}

	if !chainExists {
		// Create the chain
		if err := fm.ipt.NewChain(FilterTable, MaburVMFirewallChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", MaburVMFirewallChain, err)
		}
	}

	// Stateful accept at the TOP of the chain: return traffic for
	// outbound-initiated connections must pass before any per-VM default-drop,
	// otherwise enabling FORWARD filtering would break every established
	// connection. Idempotent; inserted at position 1.
	ctAccept := []string{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}
	if ok, err := fm.ipt.Exists(FilterTable, MaburVMFirewallChain, ctAccept...); err != nil {
		return fmt.Errorf("failed to check conntrack accept: %w", err)
	} else if !ok {
		if err := fm.ipt.Insert(FilterTable, MaburVMFirewallChain, 1, ctAccept...); err != nil {
			return fmt.Errorf("failed to add conntrack accept: %w", err)
		}
	}

	// Reach the chain from BOTH INPUT (host-destined) and FORWARD (bridged/NATed
	// guest traffic). VM traffic traverses FORWARD, so without this jump the
	// per-VM rules never match. Idempotent; only inserted when absent.
	for _, chain := range []string{InputChain, ForwardChain} {
		exists, err := fm.ipt.Exists(FilterTable, chain, "-j", MaburVMFirewallChain)
		if err != nil {
			return fmt.Errorf("failed to check %s jump: %w", chain, err)
		}
		if !exists {
			if err := fm.ipt.Insert(FilterTable, chain, 1, "-j", MaburVMFirewallChain); err != nil {
				return fmt.Errorf("failed to add %s jump: %w", chain, err)
			}
		}
	}

	return nil
}

// FromModelRule converts a models.FirewallRule to network.FirewallRule
func FromModelRule(rule models.FirewallRule) FirewallRule {
	sourceIP := rule.SourceIP
	if sourceIP == "" {
		sourceIP = "0.0.0.0/0"
	}
	return FirewallRule{
		ID:        rule.ID,
		Protocol:  rule.Protocol,
		PortRange: rule.PortRange,
		Action:    rule.Action,
		Direction: rule.Direction,
		SourceIP:  sourceIP,
		Priority:  rule.Priority,
	}
}

// ApplyFirewallRules applies a set of firewall rules for a VM
// This removes any existing rules for the VM and applies the new ones
func (fm *FirewallManager) ApplyFirewallRules(vmID string, internalIP string, rules []FirewallRule) error {
	if internalIP == "" {
		return fmt.Errorf("internal IP cannot be empty")
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	// First, remove existing rules for this VM
	if err := fm.removeVMRulesInternal(vmID); err != nil {
		return fmt.Errorf("failed to remove existing rules: %w", err)
	}

	// Initialize rules tracking for this VM
	fm.rules[vmID] = make(map[string][]string)

	// Sort rules by priority (lower number = higher priority)
	sortedRules := make([]FirewallRule, len(rules))
	copy(sortedRules, rules)
	for i := 0; i < len(sortedRules)-1; i++ {
		for j := i + 1; j < len(sortedRules); j++ {
			if sortedRules[i].Priority > sortedRules[j].Priority {
				sortedRules[i], sortedRules[j] = sortedRules[j], sortedRules[i]
			}
		}
	}

	// Apply each rule
	for _, rule := range sortedRules {
		if err := fm.applyRuleInternal(vmID, internalIP, rule); err != nil {
			// Attempt to cleanup on failure
			_ = fm.removeVMRulesInternal(vmID)
			return fmt.Errorf("failed to apply rule %s: %w", rule.ID, err)
		}
	}

	// Add default drop rule at the end (if no explicit allow rules exist, traffic is dropped)
	// This provides a default-deny policy
	defaultDropRule := []string{
		"-d", internalIP,
		"-j", "DROP",
		"-m", "comment",
		"--comment", fmt.Sprintf("maburvm-vm-%s-default-drop", vmID),
	}

	exists, err := fm.ipt.Exists(FilterTable, MaburVMFirewallChain, defaultDropRule...)
	if err != nil {
		return fmt.Errorf("failed to check default drop rule: %w", err)
	}
	if !exists {
		if err := fm.ipt.Append(FilterTable, MaburVMFirewallChain, defaultDropRule...); err != nil {
			return fmt.Errorf("failed to add default drop rule: %w", err)
		}
	}

	return nil
}

// applyRuleInternal applies a single firewall rule (assumes lock is held)
func (fm *FirewallManager) applyRuleInternal(vmID string, internalIP string, rule FirewallRule) error {
	// Validate rule
	if err := validateRule(rule); err != nil {
		return err
	}

	// Build the iptables rule
	var ruleSpec []string

	// Add protocol if specified and not "all"
	if rule.Protocol != "" && rule.Protocol != "all" {
		ruleSpec = append(ruleSpec, "-p", rule.Protocol)
	}

	// Add source IP/CIDR
	if rule.SourceIP != "" && rule.SourceIP != "0.0.0.0/0" {
		ruleSpec = append(ruleSpec, "-s", rule.SourceIP)
	}

	// Add destination (VM's IP)
	if rule.Direction == "inbound" {
		ruleSpec = append(ruleSpec, "-d", internalIP)
	} else {
		ruleSpec = append(ruleSpec, "-s", internalIP)
	}

	// Add port if specified (only for tcp/udp)
	if rule.PortRange != "" && (rule.Protocol == "tcp" || rule.Protocol == "udp") {
		if strings.Contains(rule.PortRange, ":") {
			// Port range
			ruleSpec = append(ruleSpec, "-m", "multiport", "--dports", rule.PortRange)
		} else {
			// Single port
			if _, err := strconv.Atoi(rule.PortRange); err == nil {
				ruleSpec = append(ruleSpec, "--dport", rule.PortRange)
			}
		}
	}

	// Add action
	if rule.Action == "allow" {
		ruleSpec = append(ruleSpec, "-j", "ACCEPT")
	} else {
		ruleSpec = append(ruleSpec, "-j", "DROP")
	}

	// Add comment for tracking
	ruleSpec = append(ruleSpec, "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-rule-%s", vmID, rule.ID))

	// Insert the rule (using Insert to maintain priority order)
	if err := fm.ipt.Insert(FilterTable, MaburVMFirewallChain, 1, ruleSpec...); err != nil {
		return fmt.Errorf("failed to insert rule: %w", err)
	}

	// Track the rule
	fm.rules[vmID][rule.ID] = ruleSpec

	return nil
}

// validateRule validates a firewall rule
func validateRule(rule FirewallRule) error {
	// Validate protocol
	validProtocols := map[string]bool{"tcp": true, "udp": true, "icmp": true, "all": true}
	if !validProtocols[rule.Protocol] {
		return fmt.Errorf("invalid protocol: %s", rule.Protocol)
	}

	// Validate action
	validActions := map[string]bool{"allow": true, "deny": true}
	if !validActions[rule.Action] {
		return fmt.Errorf("invalid action: %s", rule.Action)
	}

	// Validate direction
	validDirections := map[string]bool{"inbound": true, "outbound": true}
	if !validDirections[rule.Direction] {
		return fmt.Errorf("invalid direction: %s", rule.Direction)
	}

	// Validate port range if specified
	if rule.PortRange != "" {
		if strings.Contains(rule.PortRange, ":") {
			parts := strings.Split(rule.PortRange, ":")
			if len(parts) != 2 {
				return fmt.Errorf("invalid port range: %s", rule.PortRange)
			}
			start, err1 := strconv.Atoi(parts[0])
			end, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil || start < 1 || end > 65535 || start > end {
				return fmt.Errorf("invalid port range: %s", rule.PortRange)
			}
		} else {
			port, err := strconv.Atoi(rule.PortRange)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port: %s", rule.PortRange)
			}
		}
	}

	// Validate priority
	if rule.Priority < 1 || rule.Priority > 1000 {
		return fmt.Errorf("invalid priority: %d (must be 1-1000)", rule.Priority)
	}

	return nil
}

// RemoveFirewallRules removes all firewall rules for a VM
func (fm *FirewallManager) RemoveFirewallRules(vmID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	return fm.removeVMRulesInternal(vmID)
}

// removeVMRulesInternal removes all rules for a VM (assumes lock is held)
func (fm *FirewallManager) removeVMRulesInternal(vmID string) error {
	// Get current rules in the chain
	rules, err := fm.ipt.List(FilterTable, MaburVMFirewallChain)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}

	// Find and delete rules with our comment prefix
	prefix := fmt.Sprintf("maburvm-vm-%s", vmID)
	for _, rule := range rules {
		if strings.Contains(rule, prefix) {
			// Parse the rule to extract the specification
			// The rule format is: -A MABURVM-FIREWALL <rule-spec>
			parts := strings.SplitN(rule, " ", 3)
			if len(parts) >= 3 {
				ruleSpec := strings.Fields(parts[2])
				// Delete the rule
				if err := fm.ipt.Delete(FilterTable, MaburVMFirewallChain, ruleSpec...); err != nil {
					// Ignore errors for non-existent rules
					if !strings.Contains(err.Error(), "No chain/target/match by that name") {
						return fmt.Errorf("failed to delete rule: %w", err)
					}
				}
			}
		}
	}

	// Clean up tracking
	delete(fm.rules, vmID)

	return nil
}

// GetActiveRules returns the active rules for a VM
func (fm *FirewallManager) GetActiveRules(vmID string) []string {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	var result []string
	if rules, exists := fm.rules[vmID]; exists {
		for ruleID := range rules {
			result = append(result, ruleID)
		}
	}
	return result
}

// CleanupVM removes all firewall rules for a VM
// This should be called when a VM is deleted
func (fm *FirewallManager) CleanupVM(vmID string) error {
	return fm.RemoveFirewallRules(vmID)
}
