package network

import (
	"fmt"
	"log"
	"sync"

	"github.com/maburvm/panel/internal/shared/models"
)

// Manager provides a unified interface for managing all network aspects of VMs
type Manager struct {
	bandwidth *BandwidthManager
	nat       *NATManager
	firewall  *FirewallManager
	vlan      *VLANManager
	mu        sync.RWMutex
	// vmNetworks tracks network state per VM
	vmNetworks map[string]*VMNetworkState
}

// VMNetworkState holds the network configuration state for a VM
type VMNetworkState struct {
	VMID          string
	InternalIP    string
	VLANID        int
	Bandwidth     int // Mbps, 0 = unlimited
	PortForwards  map[int]portForwardEntry
	FirewallRules []string // Rule IDs
}

// NewManager creates a new network manager with all sub-managers initialized
func NewManager() (*Manager, error) {
	// Initialize bandwidth manager
	bwManager := NewBandwidthManager()

	// Initialize NAT manager
	natManager, err := NewNATManager()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize NAT manager: %w", err)
	}

	// Initialize firewall manager
	fwManager, err := NewFirewallManager()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize firewall manager: %w", err)
	}

	// Initialize VLAN manager
	vlanManager := NewVLANManager()

	return &Manager{
		bandwidth:  bwManager,
		nat:        natManager,
		firewall:   fwManager,
		vlan:       vlanManager,
		vmNetworks: make(map[string]*VMNetworkState),
	}, nil
}

// SetupVMNetwork sets up the complete network configuration for a VM
// This should be called when a VM is started
func (m *Manager) SetupVMNetwork(vmID string, internalIP string, vlanID int, bandwidthMbps int, rules []models.FirewallRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &VMNetworkState{
		VMID:         vmID,
		InternalIP:   internalIP,
		VLANID:       vlanID,
		Bandwidth:    bandwidthMbps,
		PortForwards: make(map[int]portForwardEntry),
	}

	// 1. Setup NAT (MASQUERADE)
	if err := m.nat.SetupNAT(vmID, internalIP); err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}
	log.Printf("[NetworkManager] NAT setup for VM %s (IP: %s)", vmID, internalIP)

	// 2. Apply bandwidth limit if specified
	if bandwidthMbps > 0 {
		if err := m.bandwidth.LimitBandwidth(vmID, bandwidthMbps); err != nil {
			// Cleanup NAT on failure
			_ = m.nat.RemoveNAT(vmID, internalIP)
			return fmt.Errorf("failed to apply bandwidth limit: %w", err)
		}
		log.Printf("[NetworkManager] Bandwidth limit %d Mbps applied to VM %s", bandwidthMbps, vmID)
	}

	// 3. Assign VLAN if specified
	if vlanID > 0 {
		if err := m.vlan.AssignVLAN(vmID, vlanID); err != nil {
			// Cleanup on failure
			_ = m.bandwidth.RemoveBandwidthLimit(vmID)
			_ = m.nat.RemoveNAT(vmID, internalIP)
			return fmt.Errorf("failed to assign VLAN: %w", err)
		}
		log.Printf("[NetworkManager] VLAN %d assigned to VM %s", vlanID, vmID)
	}

	// 4. Apply firewall rules
	if len(rules) > 0 {
		fwRules := make([]FirewallRule, len(rules))
		for i, rule := range rules {
			fwRules[i] = FromModelRule(rule)
			state.FirewallRules = append(state.FirewallRules, rule.ID)
		}
		if err := m.firewall.ApplyFirewallRules(vmID, internalIP, fwRules); err != nil {
			// Cleanup on failure
			_ = m.vlan.RemoveVLAN(vmID, vlanID)
			_ = m.bandwidth.RemoveBandwidthLimit(vmID)
			_ = m.nat.RemoveNAT(vmID, internalIP)
			return fmt.Errorf("failed to apply firewall rules: %w", err)
		}
		log.Printf("[NetworkManager] %d firewall rules applied to VM %s", len(rules), vmID)
	}

	// Store state
	m.vmNetworks[vmID] = state

	return nil
}

// CleanupVMNetwork removes all network configuration for a VM
// This should be called when a VM is deleted or stopped
func (m *Manager) CleanupVMNetwork(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		// Try to cleanup anyway in case state is lost
		log.Printf("[NetworkManager] No state found for VM %s, attempting best-effort cleanup", vmID)
	}

	var errs []error

	// Cleanup in reverse order of setup

	// 1. Remove firewall rules
	if err := m.firewall.CleanupVM(vmID); err != nil {
		errs = append(errs, fmt.Errorf("firewall cleanup: %w", err))
	}

	// 2. Remove VLAN assignment
	if state != nil && state.VLANID > 0 {
		if err := m.vlan.CleanupVM(vmID); err != nil {
			errs = append(errs, fmt.Errorf("VLAN cleanup: %w", err))
		}
	}

	// 3. Remove bandwidth limit
	if err := m.bandwidth.CleanupVM(vmID); err != nil {
		errs = append(errs, fmt.Errorf("bandwidth cleanup: %w", err))
	}

	// 4. Remove NAT and port forwards
	if state != nil && state.InternalIP != "" {
		if err := m.nat.CleanupVM(vmID, state.InternalIP); err != nil {
			errs = append(errs, fmt.Errorf("NAT cleanup: %w", err))
		}
	}

	// Remove state
	delete(m.vmNetworks, vmID)

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}

	log.Printf("[NetworkManager] Network cleanup completed for VM %s", vmID)
	return nil
}

// AddPortForward adds a port forward for a VM
func (m *Manager) AddPortForward(vmID string, externalPort int, internalPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if err := m.nat.AddPortForward(vmID, externalPort, state.InternalIP, internalPort); err != nil {
		return fmt.Errorf("failed to add port forward: %w", err)
	}

	state.PortForwards[externalPort] = portForwardEntry{
		internalIP:   state.InternalIP,
		internalPort: internalPort,
	}

	log.Printf("[NetworkManager] Port forward %d -> %s:%d added for VM %s",
		externalPort, state.InternalIP, internalPort, vmID)
	return nil
}

// RemovePortForward removes a port forward for a VM
func (m *Manager) RemovePortForward(vmID string, externalPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if err := m.nat.RemovePortForward(vmID, externalPort); err != nil {
		return fmt.Errorf("failed to remove port forward: %w", err)
	}

	delete(state.PortForwards, externalPort)

	log.Printf("[NetworkManager] Port forward %d removed for VM %s", externalPort, vmID)
	return nil
}

// UpdateBandwidthLimit updates the bandwidth limit for a VM
func (m *Manager) UpdateBandwidthLimit(vmID string, rateMbps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if rateMbps > 0 {
		if err := m.bandwidth.LimitBandwidth(vmID, rateMbps); err != nil {
			return fmt.Errorf("failed to apply bandwidth limit: %w", err)
		}
	} else {
		if err := m.bandwidth.RemoveBandwidthLimit(vmID); err != nil {
			return fmt.Errorf("failed to remove bandwidth limit: %w", err)
		}
	}

	state.Bandwidth = rateMbps

	log.Printf("[NetworkManager] Bandwidth limit updated to %d Mbps for VM %s", rateMbps, vmID)
	return nil
}

// UpdateFirewallRules updates the firewall rules for a VM
func (m *Manager) UpdateFirewallRules(vmID string, rules []models.FirewallRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	fwRules := make([]FirewallRule, len(rules))
	for i, rule := range rules {
		fwRules[i] = FromModelRule(rule)
	}

	if err := m.firewall.ApplyFirewallRules(vmID, state.InternalIP, fwRules); err != nil {
		return fmt.Errorf("failed to apply firewall rules: %w", err)
	}

	// Update state
	state.FirewallRules = make([]string, len(rules))
	for i, rule := range rules {
		state.FirewallRules[i] = rule.ID
	}

	log.Printf("[NetworkManager] %d firewall rules updated for VM %s", len(rules), vmID)
	return nil
}

// GetVMNetworkState returns the current network state for a VM
func (m *Manager) GetVMNetworkState(vmID string) (*VMNetworkState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return nil, false
	}

	// Return a copy
	stateCopy := &VMNetworkState{
		VMID:          state.VMID,
		InternalIP:    state.InternalIP,
		VLANID:        state.VLANID,
		Bandwidth:     state.Bandwidth,
		PortForwards:  make(map[int]portForwardEntry),
		FirewallRules: make([]string, len(state.FirewallRules)),
	}
	for k, v := range state.PortForwards {
		stateCopy.PortForwards[k] = v
	}
	copy(stateCopy.FirewallRules, state.FirewallRules)

	return stateCopy, true
}

// ListActiveVMs returns a list of VMs with active network configuration
func (m *Manager) ListActiveVMs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	vms := make([]string, 0, len(m.vmNetworks))
	for vmID := range m.vmNetworks {
		vms = append(vms, vmID)
	}
	return vms
}
