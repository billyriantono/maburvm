package network

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const (
	// IPPath is the path to the ip binary
	IPPath = "/sbin/ip"
	// BridgePath is the path to the bridge binary (for bridge vlan commands)
	BridgePath = "/sbin/bridge"
	// VLANMaxID is the maximum valid VLAN ID (12 bits = 4094, 0 and 4095 are reserved)
	VLANMaxID = 4094
)

// VLANManager handles VLAN tagging and assignment for VMs
type VLANManager struct {
	ipPath     string
	bridgePath string
	mu         sync.RWMutex
	// vlanAssignments tracks VLAN assignments: map[vmID]vlanID
	vlanAssignments map[string]int
	// vlanInterfaces tracks created VLAN interfaces: map[vlanID]interfaceName
	vlanInterfaces map[int]string
}

// VLANConfig represents VLAN configuration for a VM
type VLANConfig struct {
	VLANID          int    // 1-4094
	ParentInterface string // e.g., "eth0", "br0"
}

// NewVLANManager creates a new VLANManager instance
func NewVLANManager() *VLANManager {
	return &VLANManager{
		ipPath:          IPPath,
		bridgePath:      BridgePath,
		vlanAssignments: make(map[string]int),
		vlanInterfaces:  make(map[int]string),
	}
}

// validateVLANID checks if a VLAN ID is valid
func (vm *VLANManager) validateVLANID(vlanID int) error {
	if vlanID < 1 || vlanID > VLANMaxID {
		return fmt.Errorf("invalid VLAN ID: %d (must be 1-%d)", vlanID, VLANMaxID)
	}
	return nil
}

// getVNetInterface returns the vnet interface name for a VM
func (vm *VLANManager) getVNetInterface(vmID string) string {
	return fmt.Sprintf("%s%s", DefaultInterfacePrefix, vmID[:8])
}

// AssignVLAN assigns a VLAN ID to a VM's vnet interface
// This uses bridge VLAN filtering to tag traffic from the VM
func (vm *VLANManager) AssignVLAN(vmID string, vlanID int) error {
	if err := vm.validateVLANID(vlanID); err != nil {
		return err
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	iface := vm.getVNetInterface(vmID)

	// Method 1: Using bridge vlan commands (requires bridge-utils)
	// This adds the vnet interface to a VLAN on the bridge
	if err := vm.assignBridgeVLAN(iface, vlanID); err != nil {
		// Fallback to ip link method
		if err := vm.assignIPLinkVLAN(iface, vlanID); err != nil {
			return fmt.Errorf("failed to assign VLAN %d to %s: %w", vlanID, iface, err)
		}
	}

	// Track the assignment
	vm.vlanAssignments[vmID] = vlanID

	return nil
}

// assignBridgeVLAN uses bridge vlan commands to assign VLAN
func (vm *VLANManager) assignBridgeVLAN(iface string, vlanID int) error {
	// Check if bridge vlan is supported
	cmd := exec.Command(vm.bridgePath, "vlan", "show")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bridge vlan not supported: %w", err)
	}

	// Add VLAN to the interface
	// Format: bridge vlan add dev <iface> vid <vlanID> [pvid] [untagged]
	cmd = exec.Command(vm.bridgePath, "vlan", "add", "dev", iface, "vid", strconv.Itoa(vlanID), "pvid", "untagged")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bridge vlan add failed: %w, output: %s", err, string(output))
	}

	return nil
}

// assignIPLinkVLAN uses ip link to create VLAN interfaces as fallback
func (vm *VLANManager) assignIPLinkVLAN(iface string, vlanID int) error {
	// This creates a separate VLAN interface
	// Format: ip link add link <parent> name <parent.vlanID> type vlan id <vlanID>
	vlanIface := fmt.Sprintf("%s.%d", iface, vlanID)

	// Check if interface already exists
	cmd := exec.Command(vm.ipPath, "link", "show", vlanIface)
	if err := cmd.Run(); err == nil {
		// Interface already exists
		return nil
	}

	// Create VLAN interface
	cmd = exec.Command(vm.ipPath, "link", "add", "link", iface, "name", vlanIface, "type", "vlan", "id", strconv.Itoa(vlanID))
	if output, err := cmd.CombinedOutput(); err != nil {
		// Ignore "already exists" errors
		if !strings.Contains(string(output), "already exists") {
			return fmt.Errorf("ip link add failed: %w, output: %s", err, string(output))
		}
	}

	// Bring up the VLAN interface
	cmd = exec.Command(vm.ipPath, "link", "set", "dev", vlanIface, "up")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up failed: %w, output: %s", err, string(output))
	}

	// Track the interface
	vm.vlanInterfaces[vlanID] = vlanIface

	return nil
}

// RemoveVLAN removes VLAN assignment from a VM's vnet interface
func (vm *VLANManager) RemoveVLAN(vmID string, vlanID int) error {
	if err := vm.validateVLANID(vlanID); err != nil {
		return err
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	iface := vm.getVNetInterface(vmID)

	// Try bridge vlan first
	if err := vm.removeBridgeVLAN(iface, vlanID); err != nil {
		// Fallback to ip link
		if err := vm.removeIPLinkVLAN(vlanID); err != nil {
			return fmt.Errorf("failed to remove VLAN %d from %s: %w", vlanID, iface, err)
		}
	}

	// Remove from tracking
	delete(vm.vlanAssignments, vmID)

	return nil
}

// removeBridgeVLAN removes VLAN assignment using bridge commands
func (vm *VLANManager) removeBridgeVLAN(iface string, vlanID int) error {
	// Remove VLAN from the interface
	// Format: bridge vlan del dev <iface> vid <vlanID>
	cmd := exec.Command(vm.bridgePath, "vlan", "del", "dev", iface, "vid", strconv.Itoa(vlanID))
	if output, err := cmd.CombinedOutput(); err != nil {
		// Ignore "does not exist" errors
		if strings.Contains(string(output), "does not exist") ||
			strings.Contains(string(output), "No such device") {
			return nil
		}
		return fmt.Errorf("bridge vlan del failed: %w, output: %s", err, string(output))
	}

	return nil
}

// removeIPLinkVLAN removes VLAN interface using ip link
func (vm *VLANManager) removeIPLinkVLAN(vlanID int) error {
	vlanIface, exists := vm.vlanInterfaces[vlanID]
	if !exists {
		return nil // Nothing to remove
	}

	// Delete the VLAN interface
	// Format: ip link del <vlanIface>
	cmd := exec.Command(vm.ipPath, "link", "del", vlanIface)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Ignore "does not exist" errors
		if strings.Contains(string(output), "does not exist") ||
			strings.Contains(string(output), "No such device") {
			delete(vm.vlanInterfaces, vlanID)
			return nil
		}
		return fmt.Errorf("ip link del failed: %w, output: %s", err, string(output))
	}

	delete(vm.vlanInterfaces, vlanID)
	return nil
}

// GetVLANAssignment returns the current VLAN assignment for a VM
func (vm *VLANManager) GetVLANAssignment(vmID string) (int, bool) {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	vlanID, exists := vm.vlanAssignments[vmID]
	return vlanID, exists
}

// ListVLANAssignments returns all VLAN assignments
func (vm *VLANManager) ListVLANAssignments() map[string]int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	result := make(map[string]int)
	for vmID, vlanID := range vm.vlanAssignments {
		result[vmID] = vlanID
	}
	return result
}

// SetupBridgeVLAN enables VLAN filtering on a bridge interface
// This must be called before VLAN assignments will work on a bridge
func (vm *VLANManager) SetupBridgeVLAN(bridgeName string) error {
	// Enable VLAN filtering on the bridge
	// Format: ip link set dev <bridge> type bridge vlan_filtering 1
	cmd := exec.Command(vm.ipPath, "link", "set", "dev", bridgeName, "type", "bridge", "vlan_filtering", "1")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to enable VLAN filtering on %s: %w, output: %s", bridgeName, err, string(output))
	}

	return nil
}

// GetVLANStats returns statistics for a VLAN interface
func (vm *VLANManager) GetVLANStats(vlanID int) (map[string]string, error) {
	vlanIface, exists := vm.vlanInterfaces[vlanID]
	if !exists {
		return nil, fmt.Errorf("no interface found for VLAN %d", vlanID)
	}

	// Get interface statistics using ip -s link show
	cmd := exec.Command(vm.ipPath, "-s", "link", "show", vlanIface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}

	// Parse the output
	stats := make(map[string]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "RX:") || strings.Contains(line, "TX:") {
			// Next lines contain the stats
			continue
		}
		// Try to extract RX/TX bytes, packets, errors, dropped
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			stats["bytes"] = fields[0]
			stats["packets"] = fields[1]
			stats["errors"] = fields[2]
			stats["dropped"] = fields[3]
		}
	}

	return stats, nil
}

// CleanupVM removes all VLAN assignments for a VM
// This should be called when a VM is deleted
func (vm *VLANManager) CleanupVM(vmID string) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	vlanID, exists := vm.vlanAssignments[vmID]
	if !exists {
		return nil // Nothing to cleanup
	}

	iface := vm.getVNetInterface(vmID)

	// Try to remove bridge VLAN (ignore errors)
	_ = vm.removeBridgeVLAN(iface, vlanID)

	// Remove from tracking
	delete(vm.vlanAssignments, vmID)

	return nil
}

// CreateVLANInterface creates a new VLAN interface on a parent interface
// This is useful for creating trunk interfaces or tagged interfaces for specific VLANs
func (vm *VLANManager) CreateVLANInterface(parentIface string, vlanID int) (string, error) {
	if err := vm.validateVLANID(vlanID); err != nil {
		return "", err
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	vlanIface := fmt.Sprintf("%s.%d", parentIface, vlanID)

	// Check if already exists
	cmd := exec.Command(vm.ipPath, "link", "show", vlanIface)
	if err := cmd.Run(); err == nil {
		// Interface already exists, ensure it's up
		cmd = exec.Command(vm.ipPath, "link", "set", "dev", vlanIface, "up")
		_ = cmd.Run()
		vm.vlanInterfaces[vlanID] = vlanIface
		return vlanIface, nil
	}

	// Create the VLAN interface
	cmd = exec.Command(vm.ipPath, "link", "add", "link", parentIface, "name", vlanIface, "type", "vlan", "id", strconv.Itoa(vlanID))
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create VLAN interface: %w, output: %s", err, string(output))
	}

	// Bring it up
	cmd = exec.Command(vm.ipPath, "link", "set", "dev", vlanIface, "up")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Attempt to clean up
		_ = exec.Command(vm.ipPath, "link", "del", vlanIface).Run()
		return "", fmt.Errorf("failed to bring up VLAN interface: %w, output: %s", err, string(output))
	}

	vm.vlanInterfaces[vlanID] = vlanIface
	return vlanIface, nil
}

// DeleteVLANInterface deletes a VLAN interface
func (vm *VLANManager) DeleteVLANInterface(vlanID int) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	return vm.removeIPLinkVLAN(vlanID)
}
