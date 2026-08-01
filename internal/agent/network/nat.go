package network

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"
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
)

// NATManager handles NAT and port forwarding operations
type NATManager struct {
	ipt *iptables.IPTables
	mu  sync.RWMutex
	// portForwards tracks active port forwards: map[vmID]map[externalPort]{internalIP, internalPort}
	portForwards map[string]map[int]portForwardEntry
}

type portForwardEntry struct {
	internalIP   string
	internalPort int
	protocol     string
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

	return nm, nil
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

// SetupNAT sets up MASQUERADE for a VM's internal IP
// This allows the VM to access external networks through NAT
func (nm *NATManager) SetupNAT(vmID string, internalIP string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Validate IP address
	if internalIP == "" {
		return fmt.Errorf("internal IP cannot be empty")
	}

	// Add MASQUERADE rule for outgoing traffic from the VM
	// Format: -t nat -A POSTROUTING -s <internalIP> -j MASQUERADE
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

	// Enable IP forwarding if not already enabled (this is a system-wide setting)
	// In production, this should be done during agent initialization
	// For now, we just ensure the iptables rules are in place

	// Add FORWARD rule to allow traffic from/to this VM
	forwardRule := []string{"-s", internalIP, "-j", "ACCEPT", "-m", "comment", "--comment", fmt.Sprintf("maburvm-vm-%s-out", vmID)}
	exists, err = nm.ipt.Exists(FilterTable, ForwardChain, forwardRule...)
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

// AddPortForward adds a port forwarding rule (DNAT)
// externalPort: port on the host that will be forwarded
// internalIP: IP address of the VM
// internalPort: port on the VM to forward to
func (nm *NATManager) AddPortForward(vmID string, externalPort int, internalIP string, internalPort int, protocol string) error {
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

	// Build DNAT rule
	// Format: -t nat -A MABURVM-NAT -p <proto> --dport <externalPort> -j DNAT --to-destination <internalIP>:<internalPort>
	rule := []string{
		"-p", protocol,
		"--dport", strconv.Itoa(externalPort),
		"-j", "DNAT",
		"--to-destination", fmt.Sprintf("%s:%d", internalIP, internalPort),
		"-m", "comment",
		"--comment", fmt.Sprintf("maburvm-vm-%s-port-%d", vmID, externalPort),
	}

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
		// Try to delete anyway in case tracking is out of sync (try both protocols).
		for _, proto := range []string{"tcp", "udp"} {
			rule := []string{
				"-p", proto,
				"--dport", strconv.Itoa(externalPort),
				"-j", "DNAT",
				"-m", "comment",
				"--comment", fmt.Sprintf("maburvm-vm-%s-port-%d", vmID, externalPort),
			}
			if err := nm.ipt.Delete(NATTable, MaburVMChain, rule...); err != nil {
				if !strings.Contains(err.Error(), "No chain/target/match by that name") {
					return fmt.Errorf("failed to delete port forward rule: %w", err)
				}
			}
		}
		return nil
	}

	proto := entry.protocol
	if proto == "" {
		proto = "tcp"
	}
	// Build and delete the rule
	rule := []string{
		"-p", proto,
		"--dport", strconv.Itoa(externalPort),
		"-j", "DNAT",
		"--to-destination", fmt.Sprintf("%s:%d", entry.internalIP, entry.internalPort),
		"-m", "comment",
		"--comment", fmt.Sprintf("maburvm-vm-%s-port-%d", vmID, externalPort),
	}

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
