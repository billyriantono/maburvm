package network

import (
	"fmt"
	"log"
	"sync"

	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/shared/models"
)

// Manager provides a unified interface for managing all network aspects of VMs
type Manager struct {
	bandwidth *BandwidthManager
	nat       *NATManager
	firewall  *FirewallManager
	vlan      *VLANManager
	antiSpoof *AntiSpoofManager
	mu        sync.RWMutex
	// vmNetworks tracks network state per VM
	vmNetworks map[string]*VMNetworkState
}

// VMNetworkState holds the network configuration state for a VM
type VMNetworkState struct {
	VMID             string
	InternalIP       string
	MACAddress       string
	VLANID           int
	Bandwidth        int // Mbps, 0 = unlimited
	PortForwards     map[int]portForwardEntry
	FirewallRules    []string // Rule IDs
	AntiSpoofEnabled bool
}

// NewManager creates a new network manager with all sub-managers initialized
func NewManager() (*Manager, error) {
	// Every iptables rule this manager installs — floating IP NAT, port
	// forwards, and the per-VM firewall chains — is a no-op unless the kernel is
	// actually routing and passing bridged frames through netfilter. Today that
	// happens to be true because Docker/libvirt usually enable both, so a node
	// booting without them would silently drop all of it.
	ensureForwardingSysctls()

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

	// Initialize Anti-Spoof manager
	asManager, err := NewAntiSpoofManager()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Anti-Spoof manager: %w", err)
	}

	return &Manager{
		bandwidth:  bwManager,
		nat:        natManager,
		firewall:   fwManager,
		vlan:       vlanManager,
		antiSpoof:  asManager,
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

	// 2. Apply bandwidth limit via libvirt so BOTH download and upload are
	//    shaped (the manual tc HTB only caps download). 0 = unlimited.
	if err := m.applyBandwidth(vmID, bandwidthMbps); err != nil {
		// Cleanup NAT on failure
		_ = m.nat.RemoveNAT(vmID, internalIP)
		return fmt.Errorf("failed to apply bandwidth limit: %w", err)
	}
	if bandwidthMbps > 0 {
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

	// 1. Remove anti-spoofing rules
	if err := m.antiSpoof.CleanupVM(vmID); err != nil {
		errs = append(errs, fmt.Errorf("anti-spoof cleanup: %w", err))
	}

	// 2. Remove firewall rules
	if err := m.firewall.CleanupVM(vmID); err != nil {
		errs = append(errs, fmt.Errorf("firewall cleanup: %w", err))
	}

	// 3. Remove VLAN assignment
	if state != nil && state.VLANID > 0 {
		if err := m.vlan.CleanupVM(vmID); err != nil {
			errs = append(errs, fmt.Errorf("VLAN cleanup: %w", err))
		}
	}

	// 4. Remove bandwidth limit
	if err := m.bandwidth.CleanupVM(vmID); err != nil {
		errs = append(errs, fmt.Errorf("bandwidth cleanup: %w", err))
	}

	// 5. Remove NAT and port forwards
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

// AddPortForward adds a port forward for a VM. protocol is "tcp" or "udp"
// (empty defaults to tcp).
func (m *Manager) AddPortForward(vmID string, externalPort int, internalPort int, protocol, sourceCIDR string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if err := m.nat.AddPortForward(vmID, externalPort, state.InternalIP, internalPort, protocol, sourceCIDR); err != nil {
		return fmt.Errorf("failed to add port forward: %w", err)
	}

	state.PortForwards[externalPort] = portForwardEntry{
		internalIP:   state.InternalIP,
		internalPort: internalPort,
		protocol:     protocol,
		sourceCIDR:   sourceCIDR,
	}

	log.Printf("[NetworkManager] Port forward %d -> %s:%d (%s) added for VM %s",
		externalPort, state.InternalIP, internalPort, protocol, vmID)
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

	if err := m.applyBandwidth(vmID, rateMbps); err != nil {
		return fmt.Errorf("failed to update bandwidth limit: %w", err)
	}

	state.Bandwidth = rateMbps

	log.Printf("[NetworkManager] Bandwidth limit updated to %d Mbps for VM %s", rateMbps, vmID)
	return nil
}

// applyBandwidth sets a VM's speed limit in BOTH directions via libvirt, which
// caps download (tap egress HTB) and upload (tap ingress policing/IFB) alike.
// rateMbps <= 0 clears the limit. If libvirt is unavailable it falls back to the
// manual tc HTB so download shaping still degrades safely (upload can't be
// shaped without libvirt/IFB, so that case is logged).
func (m *Manager) applyBandwidth(vmID string, rateMbps int) error {
	if err := libvirt.SetInterfaceBandwidth(vmID, rateMbps); err != nil {
		log.Printf("[NetworkManager] libvirt bandwidth update failed for VM %s (%v); falling back to tc egress-only (upload unchanged)", vmID, err)
		if rateMbps > 0 {
			return m.bandwidth.LimitBandwidth(vmID, rateMbps)
		}
		return m.bandwidth.RemoveBandwidthLimit(vmID)
	}
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
		VMID:             state.VMID,
		InternalIP:       state.InternalIP,
		MACAddress:       state.MACAddress,
		VLANID:           state.VLANID,
		Bandwidth:        state.Bandwidth,
		PortForwards:     make(map[int]portForwardEntry),
		FirewallRules:    make([]string, len(state.FirewallRules)),
		AntiSpoofEnabled: state.AntiSpoofEnabled,
	}
	for k, v := range state.PortForwards {
		stateCopy.PortForwards[k] = v
	}
	copy(stateCopy.FirewallRules, state.FirewallRules)

	return stateCopy, true
}

// EnableAntiSpoofing applies anti-spoofing rules for a VM.
// This should be called after VM start, once the vnet interface is known.
// mac: VM's MAC address, vnetInterface: the tap device name (e.g. vnet0)
func (m *Manager) EnableAntiSpoofing(vmID, ip, ipv6, mac, vnetInterface string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if err := m.antiSpoof.ApplyAntiSpoofRules(vmID, ip, ipv6, mac, vnetInterface); err != nil {
		return fmt.Errorf("failed to apply anti-spoof rules: %w", err)
	}

	state.AntiSpoofEnabled = true
	state.MACAddress = mac

	log.Printf("[NetworkManager] Anti-spoof rules applied for VM %s (IP: %s, MAC: %s, Interface: %s)",
		vmID, ip, mac, vnetInterface)
	return nil
}

// DisableAntiSpoofing removes anti-spoofing rules for a VM
func (m *Manager) DisableAntiSpoofing(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, exists := m.vmNetworks[vmID]
	if !exists {
		return fmt.Errorf("VM %s not found in network state", vmID)
	}

	if err := m.antiSpoof.RemoveAntiSpoofRules(vmID); err != nil {
		return fmt.Errorf("failed to remove anti-spoof rules: %w", err)
	}

	state.AntiSpoofEnabled = false

	log.Printf("[NetworkManager] Anti-spoof rules removed for VM %s", vmID)
	return nil
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

// AttachFloatingIP binds a floating IP on the host and 1:1-NATs it to internalIP.
// Deliberately independent of m.vmNetworks: a floating IP can point at any VM on
// the node, including an imported one the agent never provisioned, so the panel
// supplies the VM's address rather than the agent looking it up.
func (m *Manager) AttachFloatingIP(vmID, floatingIP, internalIP, bridge, natMode string) error {
	if err := m.nat.AttachFloatingIP(floatingIP, internalIP, bridge, natMode); err != nil {
		return err
	}
	log.Printf("[NetworkManager] Floating IP %s -> %s (%s) attached for VM %s", floatingIP, internalIP, natMode, vmID)
	return nil
}

// DetachFloatingIP removes a floating IP's rules and host address. The VM's
// baseline masquerade is untouched, so it keeps outbound internet.
func (m *Manager) DetachFloatingIP(vmID, floatingIP, bridge string) error {
	if err := m.nat.DetachFloatingIP(floatingIP, bridge); err != nil {
		return err
	}
	log.Printf("[NetworkManager] Floating IP %s detached (was VM %s)", floatingIP, vmID)
	return nil
}

// CreateVPC provisions a tenant VPC on this node and returns the bridge guests
// attach to. Idempotent, so it doubles as the repair path after a node reboot.
func (m *Manager) CreateVPC(vpcID, subnet, gateway string) (string, error) {
	bridge, err := m.nat.CreateVPC(vpcID, subnet, gateway)
	if err != nil {
		return "", err
	}
	log.Printf("[NetworkManager] VPC %s up: subnet=%s gw=%s bridge=%s", vpcID, subnet, gateway, bridge)
	return bridge, nil
}

// DeleteVPC removes a tenant VPC's namespace, bridge and links.
func (m *Manager) DeleteVPC(vpcID string) error {
	if err := m.nat.DeleteVPC(vpcID); err != nil {
		return err
	}
	log.Printf("[NetworkManager] VPC %s removed", vpcID)
	return nil
}

// AttachFloatingIPVPC points a floating IP at a guest inside a tenant VPC. The
// address is configured in the VPC's router namespace, since the host has no
// route to the tenant's subnet.
func (m *Manager) AttachFloatingIPVPC(vpcID, floatingIP, internalIP, bridge, gateway string) error {
	if err := m.nat.AttachFloatingIPVPC(vpcID, floatingIP, internalIP, bridge, gateway); err != nil {
		return err
	}
	log.Printf("[NetworkManager] Floating IP %s -> %s inside VPC %s", floatingIP, internalIP, vpcID)
	return nil
}

// VPCFloatingMAC exposes the MAC that answers for a VPC's floating IPs, so the
// agent can announce the address from the root namespace with its own GARP.
func (m *Manager) VPCFloatingMAC(vpcID string) (string, error) { return VPCFloatingMAC(vpcID) }

// DetachFloatingIPVPC removes a floating IP from a VPC's router namespace.
func (m *Manager) DetachFloatingIPVPC(vpcID, floatingIP string) error {
	if err := m.nat.DetachFloatingIPVPC(vpcID, floatingIP); err != nil {
		return err
	}
	log.Printf("[NetworkManager] Floating IP %s detached from VPC %s", floatingIP, vpcID)
	return nil
}
