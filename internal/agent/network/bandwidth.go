// Package network provides network control functionality for VMs including
// bandwidth limiting, NAT, port forwarding, firewall rules, and VLAN tagging.
package network

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// TCPath is the path to the tc binary
	TCPath = "/sbin/tc"
	// DefaultInterfacePrefix is the default prefix for VM network interfaces
	DefaultInterfacePrefix = "vnet"
)

// BandwidthManager handles traffic control operations for VM bandwidth limiting
type BandwidthManager struct {
	tcPath string
}

// NewBandwidthManager creates a new BandwidthManager instance
func NewBandwidthManager() *BandwidthManager {
	return &BandwidthManager{
		tcPath: TCPath,
	}
}

// getVNetInterface returns the vnet interface name for a VM
// In libvirt, VM interfaces are typically named vnet0, vnet1, etc.
// The mapping from VM ID to vnet interface can be retrieved from libvirt
func (bm *BandwidthManager) getVNetInterface(vmID string) string {
	// For now, we assume the interface name can be derived from VM ID
	// In production, this should query libvirt for the actual interface name
	return fmt.Sprintf("%s%s", DefaultInterfacePrefix, vmID[:8])
}

// LimitBandwidth applies an HTB qdisc to limit bandwidth on a VM's vnet interface
// rateMbps is the bandwidth limit in Mbps (e.g., 100 for 100 Mbps)
func (bm *BandwidthManager) LimitBandwidth(vmID string, rateMbps int) error {
	if rateMbps <= 0 {
		return fmt.Errorf("rate must be positive, got %d", rateMbps)
	}

	iface := bm.getVNetInterface(vmID)

	// Convert Mbps to kbps for tc (tc uses kbps)
	rateKbps := rateMbps * 1000

	// Step 1: Delete any existing qdisc on the interface (ignore errors if none exists)
	_ = bm.deleteQdisc(iface)

	// Step 2: Add HTB qdisc as root
	// Format: tc qdisc add dev <iface> root handle 1: htb default 1
	cmd := exec.Command(bm.tcPath, "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "1")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add HTB qdisc: %w, output: %s", err, string(output))
	}

	// Step 3: Add HTB class with rate limit
	// Format: tc class add dev <iface> parent 1: classid 1:1 htb rate <rate>kbps
	cmd = exec.Command(bm.tcPath, "class", "add", "dev", iface, "parent", "1:", "classid", "1:1", "htb", "rate", fmt.Sprintf("%dkbps", rateKbps))
	if output, err := cmd.CombinedOutput(); err != nil {
		// Attempt to clean up qdisc on failure
		_ = bm.deleteQdisc(iface)
		return fmt.Errorf("failed to add HTB class: %w, output: %s", err, string(output))
	}

	return nil
}

// RemoveBandwidthLimit removes the bandwidth limit (qdisc) from a VM's vnet interface
func (bm *BandwidthManager) RemoveBandwidthLimit(vmID string) error {
	iface := bm.getVNetInterface(vmID)
	return bm.deleteQdisc(iface)
}

// deleteQdisc removes all qdiscs from an interface
func (bm *BandwidthManager) deleteQdisc(iface string) error {
	// Format: tc qdisc del dev <iface> root
	cmd := exec.Command(bm.tcPath, "qdisc", "del", "dev", iface, "root")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Check if it's just "No such file or directory" which means no qdisc exists
		if strings.Contains(string(output), "No such file or directory") ||
			strings.Contains(string(output), "RTNETLINK answers: No such file or directory") {
			return nil // No qdisc to delete, not an error
		}
		return fmt.Errorf("failed to delete qdisc: %w, output: %s", err, string(output))
	}
	return nil
}

// GetBandwidthLimit retrieves the current bandwidth limit for a VM
// Returns 0 if no limit is set, or the rate in Mbps
func (bm *BandwidthManager) GetBandwidthLimit(vmID string) (int, error) {
	iface := bm.getVNetInterface(vmID)

	// Format: tc class show dev <iface>
	cmd := exec.Command(bm.tcPath, "class", "show", "dev", iface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get class info: %w", err)
	}

	// Parse output to find rate
	// Example output: class htb 1:1 root leaf 2: prio 0 rate 100000Kbit ceil 100000Kbit burst 1599b cburst 1599b
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "htb") && strings.Contains(line, "rate") {
			// Extract rate value
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "rate" && i+1 < len(parts) {
					rateStr := parts[i+1]
					// Remove unit suffix (Kbit, Mbit, etc.)
					rateStr = strings.TrimSuffix(rateStr, "Kbit")
					rateStr = strings.TrimSuffix(rateStr, "Mbit")
					rateStr = strings.TrimSuffix(rateStr, "kbps")
					rateStr = strings.TrimSuffix(rateStr, "mbps")

					if rate, err := strconv.Atoi(rateStr); err == nil {
						// Convert to Mbps
						if strings.Contains(parts[i+1], "K") {
							return rate / 1000, nil
						}
						return rate, nil
					}
				}
			}
		}
	}

	return 0, nil // No limit set
}

// CleanupVM removes all network limits and rules for a VM
// This should be called when a VM is deleted
func (bm *BandwidthManager) CleanupVM(vmID string) error {
	return bm.RemoveBandwidthLimit(vmID)
}
