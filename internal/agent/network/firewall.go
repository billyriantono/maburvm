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
func (fm *FirewallManager) ApplyFirewallRules(vmID string, internalIP string, rules []FirewallRule, extraIPs ...string) error {
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

	// A VM with several addresses gets the same policy on each of them; an
	// address the rules don't mention would otherwise sit wide open beside a
	// locked-down primary.
	ips := append([]string{internalIP}, extraIPs...)

	// Apply each rule
	for _, rule := range sortedRules {
		for _, ip := range ips {
			if err := fm.applyRuleInternal(vmID, ip, rule); err != nil {
				// Attempt to cleanup on failure
				_ = fm.removeVMRulesInternal(vmID)
				return fmt.Errorf("failed to apply rule %s: %w", rule.ID, err)
			}
		}
	}

	// Add default drop rule at the end (if no explicit allow rules exist, traffic is dropped)
	// This provides a default-deny policy
	for _, ip := range ips {
		if err := fm.appendDefaultDrop(vmID, ip); err != nil {
			return err
		}
	}
	return nil
}

func (fm *FirewallManager) appendDefaultDrop(vmID, ip string) error {
	defaultDropRule := []string{
		"-d", ip,
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

// maxMultiportEntries is iptables' own limit on -m multiport --dports.
const maxMultiportEntries = 15

// normalizePortSpec converts the panel's port syntax into the one iptables
// speaks, and validates it.
//
// The panel — and the admin UI that feeds it — accept "80", "1000-11000" and
// "80,443,8080". iptables writes a range with a colon, and needs -m multiport
// for anything that is not a single port. The agent used to understand only
// the colon form, so a hyphenated range was rejected outright; because rules
// are applied as a set, that single rejection aborted the whole apply and left
// the VM with no rules at all rather than with the other, valid ones.
//
// Returns the iptables-form spec and whether multiport is required.
func normalizePortSpec(portRange string) (string, bool, error) {
	spec := strings.ReplaceAll(portRange, "-", ":")
	multi := strings.ContainsAny(spec, ":,")

	entries := strings.Split(spec, ",")
	if len(entries) > maxMultiportEntries {
		return "", false, fmt.Errorf("too many port entries (iptables allows %d): %s", maxMultiportEntries, portRange)
	}
	for _, entry := range entries {
		bounds := strings.Split(entry, ":")
		if len(bounds) > 2 {
			return "", false, fmt.Errorf("invalid port range: %s", portRange)
		}
		prev := 0
		for _, b := range bounds {
			port, err := strconv.Atoi(b)
			if err != nil || port < 1 || port > 65535 {
				return "", false, fmt.Errorf("invalid port: %s", portRange)
			}
			// A range must ascend; "11000-1000" silently matches nothing in
			// iptables, which is worse than refusing it.
			if prev != 0 && port < prev {
				return "", false, fmt.Errorf("invalid port range: %s", portRange)
			}
			prev = port
		}
	}
	return spec, multi, nil
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
		spec, multi, err := normalizePortSpec(rule.PortRange)
		if err != nil {
			return err
		}
		if multi {
			ruleSpec = append(ruleSpec, "-m", "multiport", "--dports", spec)
		} else {
			ruleSpec = append(ruleSpec, "--dport", spec)
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
		if _, _, err := normalizePortSpec(rule.PortRange); err != nil {
			return err
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
