// Package network provides network control functionality for VMs including
// bandwidth limiting, NAT, port forwarding, firewall rules, and VLAN tagging.
package network

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/maburvm/panel/internal/agent/libvirt"
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

// getVNetInterface returns the vnet interface name for a VM by querying libvirt
func (bm *BandwidthManager) getVNetInterface(vmID string) (string, error) {
	// Query libvirt for the actual vnet interface name
	iface, err := libvirt.GetVMInterfaceName(vmID)
	if err != nil {
		return "", fmt.Errorf("failed to get interface for VM %s: %w", vmID, err)
	}
	return iface, nil
}

// LimitBandwidth applies an HTB qdisc to limit bandwidth on a VM's vnet interface
// rateMbps is the bandwidth limit in Mbps (e.g., 100 for 100 Mbps)
func (bm *BandwidthManager) LimitBandwidth(vmID string, rateMbps int) error {
	if rateMbps <= 0 {
		return fmt.Errorf("rate must be positive, got %d", rateMbps)
	}

	iface, err := bm.getVNetInterface(vmID)
	if err != nil {
		return err
	}

	// Convert Mbps to kbit for tc. IMPORTANT: tc's "kbps" suffix means
	// kilo*bytes* per second, not kilobits — using it makes the applied limit 8x
	// the intended speed. Bandwidth is quoted in bits (a "100 Mbps" plan = 100
	// megabits/sec), so we emit bit-based units: 1 Mbps = 1000 kbit.
	rateKbit := rateMbps * 1000

	// Step 1: Delete any existing qdisc on the interface (ignore errors if none exists)
	_ = bm.deleteQdisc(iface)

	// Step 2: Add HTB qdisc as root
	cmd := exec.Command(bm.tcPath, "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "1")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add HTB qdisc: %w, output: %s", err, string(output))
	}

	// Step 3: Add HTB class with rate limit. "ceil" equal to "rate" caps the
	// burst so the interface can't briefly exceed the plan (e.g. a 10 Gbps tier
	// stays at 10 Gbps rather than borrowing unused parent bandwidth).
	rateArg := fmt.Sprintf("%dkbit", rateKbit)
	cmd = exec.Command(bm.tcPath, "class", "add", "dev", iface, "parent", "1:", "classid", "1:1", "htb", "rate", rateArg, "ceil", rateArg)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = bm.deleteQdisc(iface)
		return fmt.Errorf("failed to add HTB class: %w, output: %s", err, string(output))
	}

	return nil
}

// RemoveBandwidthLimit removes the bandwidth limit (qdisc) from a VM's vnet interface
func (bm *BandwidthManager) RemoveBandwidthLimit(vmID string) error {
	iface, err := bm.getVNetInterface(vmID)
	if err != nil {
		return err
	}
	return bm.deleteQdisc(iface)
}

// deleteQdisc removes all qdiscs from an interface
func (bm *BandwidthManager) deleteQdisc(iface string) error {
	cmd := exec.Command(bm.tcPath, "qdisc", "del", "dev", iface, "root")
	if output, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "No such file or directory") ||
			strings.Contains(string(output), "RTNETLINK answers: No such file or directory") {
			return nil
		}
		return fmt.Errorf("failed to delete qdisc: %w, output: %s", err, string(output))
	}
	return nil
}

// CleanupVM removes all network limits and rules for a VM
func (bm *BandwidthManager) CleanupVM(vmID string) error {
	return bm.RemoveBandwidthLimit(vmID)
}
