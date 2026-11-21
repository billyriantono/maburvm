package network

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"

	"github.com/maburvm/panel/internal/agent/netrules"
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
	// ipt6 mirrors the chain in ip6tables for VMs with a routed IPv6 prefix;
	// nil when the host has no ip6tables, in which case v6 is simply not
	// filtered (and no VM should be given a prefix on such a node).
	ipt6 *iptables.IPTables
	mu   sync.Mutex
}

// NewFirewallManager creates a new FirewallManager instance
func NewFirewallManager() (*FirewallManager, error) {
	ipt, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4))
	if err != nil {
		return nil, fmt.Errorf("failed to create iptables client: %w", err)
	}

	fm := &FirewallManager{ipt: ipt}

	// Ensure our custom chain exists
	if _, err := fm.ensureChain(); err != nil {
		return nil, err
	}

	// Same layout in ip6tables. Routed v6 crosses the host's FORWARD chain, so
	// this needs no br_netfilter to bite. Best-effort: v4 filtering must not
	// depend on the host having ip6tables.
	if ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6)); err == nil {
		fm.ipt6 = ipt6
		if _, err := fm.ensureChain6(); err != nil {
			log.Printf("[Firewall] WARNING: ip6tables chain not set up (%v); IPv6 prefixes will not be firewalled", err)
			fm.ipt6 = nil
		}
	}

	return fm, nil
}

// ensureChain6 is ensureChain for ip6tables, plus a blanket ICMPv6 accept:
// neighbour discovery and path MTU must never be caught by a VM's default
// drop, or the routed prefix stops working the moment it is firewalled.
func (fm *FirewallManager) ensureChain6() (bool, error) {
	changed := false
	chains, err := fm.ipt6.ListChains(FilterTable)
	if err != nil {
		return false, fmt.Errorf("failed to list chains: %w", err)
	}
	exists := false
	for _, chain := range chains {
		if chain == MaburVMFirewallChain {
			exists = true
			break
		}
	}
	if !exists {
		if err := fm.ipt6.NewChain(FilterTable, MaburVMFirewallChain); err != nil {
			return false, fmt.Errorf("failed to create chain %s: %w", MaburVMFirewallChain, err)
		}
		changed = true
	}
	for _, top := range [][]string{
		{"-p", "icmpv6", "-j", "ACCEPT"},
		{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	} {
		if ok, err := fm.ipt6.Exists(FilterTable, MaburVMFirewallChain, top...); err != nil {
			return changed, fmt.Errorf("failed to check chain head: %w", err)
		} else if !ok {
			if err := fm.ipt6.Insert(FilterTable, MaburVMFirewallChain, 1, top...); err != nil {
				return changed, fmt.Errorf("failed to add chain head: %w", err)
			}
			changed = true
		}
	}
	for _, chain := range []string{InputChain, ForwardChain} {
		exists, err := fm.ipt6.Exists(FilterTable, chain, "-j", MaburVMFirewallChain)
		if err != nil {
			return changed, fmt.Errorf("failed to check %s jump: %w", chain, err)
		}
		if !exists {
			if err := fm.ipt6.Insert(FilterTable, chain, 1, "-j", MaburVMFirewallChain); err != nil {
				return changed, fmt.Errorf("failed to add %s jump: %w", chain, err)
			}
			changed = true
		}
	}
	return changed, nil
}

// ensureChain creates the custom MaburVM firewall chain if it doesn't exist
func (fm *FirewallManager) ensureChain() (bool, error) {
	changed := false
	chains, err := fm.ipt.ListChains(FilterTable)
	if err != nil {
		return false, fmt.Errorf("failed to list chains: %w", err)
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
			return false, fmt.Errorf("failed to create chain %s: %w", MaburVMFirewallChain, err)
		}
		changed = true
	}

	// Stateful accept at the TOP of the chain: return traffic for
	// outbound-initiated connections must pass before any per-VM default-drop,
	// otherwise enabling FORWARD filtering would break every established
	// connection. Idempotent; inserted at position 1.
	ctAccept := []string{"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}
	if ok, err := fm.ipt.Exists(FilterTable, MaburVMFirewallChain, ctAccept...); err != nil {
		return changed, fmt.Errorf("failed to check conntrack accept: %w", err)
	} else if !ok {
		if err := fm.ipt.Insert(FilterTable, MaburVMFirewallChain, 1, ctAccept...); err != nil {
			return changed, fmt.Errorf("failed to add conntrack accept: %w", err)
		}
		changed = true
	}

	// Reach the chain from BOTH INPUT (host-destined) and FORWARD (bridged/NATed
	// guest traffic). VM traffic traverses FORWARD, so without this jump the
	// per-VM rules never match. Idempotent; only inserted when absent.
	for _, chain := range []string{InputChain, ForwardChain} {
		exists, err := fm.ipt.Exists(FilterTable, chain, "-j", MaburVMFirewallChain)
		if err != nil {
			return changed, fmt.Errorf("failed to check %s jump: %w", chain, err)
		}
		if !exists {
			if err := fm.ipt.Insert(FilterTable, chain, 1, "-j", MaburVMFirewallChain); err != nil {
				return changed, fmt.Errorf("failed to add %s jump: %w", chain, err)
			}
			changed = true
		}
	}

	return changed, nil
}

// EnsureChains puts the chain, its conntrack accept and the INPUT/FORWARD jumps
// back if something outside the agent removed them, and says whether it had to.
func (fm *FirewallManager) EnsureChains() (bool, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	changed, err := fm.ensureChain()
	if err != nil || fm.ipt6 == nil {
		return changed, err
	}
	c6, err := fm.ensureChain6()
	return changed || c6, err
}

// Apply makes the host filter a VM exactly as fw says.
//
// The new set goes in beside the old one under the other generation tag, with
// its rules at the head of the chain where they match first, and only then is
// the old set removed by its exact spec. So a re-apply never leaves the VM
// without rules, and a set that fails to go in leaves the old one in force.
// A VM without rules (and no mail block) ends up with no entries at all.
func (fm *FirewallManager) Apply(fw netrules.VMFirewall) error {
	if len(fw.IPs) == 0 {
		return fmt.Errorf("internal IP cannot be empty")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()

	cur4, err := fm.ipt.List(FilterTable, MaburVMFirewallChain)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}
	var cur6 []string
	if fm.ipt6 != nil {
		if cur6, err = fm.ipt6.List(FilterTable, MaburVMFirewallChain); err != nil {
			return fmt.Errorf("failed to list ip6tables rules: %w", err)
		}
	} else {
		fw.IPv6Prefix = "" // nothing to filter IPv6 with on this host
	}
	gen := netrules.NextGeneration(append(append([]string{}, cur4...), cur6...), fw.VMID)
	v4, v6, err := netrules.BuildFirewall(fw, gen)
	if err != nil {
		return err
	}

	newTag := netrules.VMRuleTag(fw.VMID) + gen + "-"
	if err := installSpecs(fm.ipt, v4); err != nil {
		removeTagged(fm.ipt, newTag)
		return fmt.Errorf("failed to insert rule: %w", err)
	}
	if fm.ipt6 != nil {
		if err := installSpecs(fm.ipt6, v6); err != nil {
			removeTagged(fm.ipt, newTag)
			removeTagged(fm.ipt6, newTag)
			return fmt.Errorf("failed to insert ip6tables rule: %w", err)
		}
	}
	oldTag := netrules.VMRuleTag(fw.VMID)
	removeLines(fm.ipt, cur4, oldTag)
	if fm.ipt6 != nil {
		removeLines(fm.ipt6, cur6, oldTag)
	}
	return nil
}

// installSpecs inserts Top at the head of the chain in order and appends Bottom.
func installSpecs(ipt *iptables.IPTables, s netrules.FirewallSpecs) error {
	for i := len(s.Top) - 1; i >= 0; i-- {
		if err := ipt.Insert(FilterTable, MaburVMFirewallChain, 1, s.Top[i]...); err != nil {
			return err
		}
	}
	for _, spec := range s.Bottom {
		if err := ipt.Append(FilterTable, MaburVMFirewallChain, spec...); err != nil {
			return err
		}
	}
	return nil
}

// removeTagged deletes every entry of the chain carrying tag.
func removeTagged(ipt *iptables.IPTables, tag string) {
	lines, err := ipt.List(FilterTable, MaburVMFirewallChain)
	if err != nil {
		log.Printf("[Firewall] WARNING: listing %s to remove %s: %v", MaburVMFirewallChain, tag, err)
		return
	}
	removeLines(ipt, lines, tag)
}

// removeLines deletes the listed entries that carry tag, by their exact spec.
func removeLines(ipt *iptables.IPTables, lines []string, tag string) {
	for _, line := range lines {
		if !strings.Contains(line, tag) {
			continue
		}
		spec := netrules.LineSpec(line)
		if spec == nil {
			continue
		}
		if err := ipt.Delete(FilterTable, MaburVMFirewallChain, spec...); err != nil &&
			!strings.Contains(err.Error(), "No chain/target/match by that name") &&
			!strings.Contains(err.Error(), "does a matching rule exist") {
			log.Printf("[Firewall] WARNING: could not remove %q: %v", line, err)
		}
	}
}

// Lists returns the chain as iptables and ip6tables list it now (v6 nil when
// the host has no ip6tables), for the drift check.
func (fm *FirewallManager) Lists() (v4, v6 []string, have6 bool) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	v4, _ = fm.ipt.List(FilterTable, MaburVMFirewallChain)
	if fm.ipt6 != nil {
		v6, _ = fm.ipt6.List(FilterTable, MaburVMFirewallChain)
	}
	return v4, v6, fm.ipt6 != nil
}

// RemoveFirewallRules removes all firewall rules for a VM
func (fm *FirewallManager) RemoveFirewallRules(vmID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	tag := netrules.VMRuleTag(vmID)
	removeTagged(fm.ipt, tag)
	if fm.ipt6 != nil {
		removeTagged(fm.ipt6, tag)
	}
	return nil
}

// CleanupVM removes all firewall rules for a VM
// This should be called when a VM is deleted
func (fm *FirewallManager) CleanupVM(vmID string) error {
	return fm.RemoveFirewallRules(vmID)
}
