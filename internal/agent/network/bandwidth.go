// Package network provides network control functionality for VMs including
// bandwidth limiting, NAT, port forwarding, firewall rules, and VLAN tagging.
package network

import (
	"fmt"
	"os/exec"
	"strconv"
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

// GetBandwidthLimit retrieves the current bandwidth limit for a VM
func (bm *BandwidthManager) GetBandwidthLimit(vmID string) (int, error) {
	iface, err := bm.getVNetInterface(vmID)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(bm.tcPath, "class", "show", "dev", iface)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get class info: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "htb") && strings.Contains(line, "rate") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "rate" && i+1 < len(parts) {
					return parseRateToMbps(parts[i+1]), nil
				}
			}
		}
	}

	return 0, nil
}

// parseRateToMbps converts a tc rate token (e.g. "10Gbit", "100Mbit",
// "500Kbit") to Mbps, rounding to the nearest integer. tc may print
// bit ("Kbit"/"Mbit"/"Gbit") or byte ("Kbps"/"Mbps") units depending on
// version; only bit units are emitted by LimitBandwidth. Returns 0 when the
// token can't be parsed.
func parseRateToMbps(token string) int {
	// Split the numeric prefix from the unit suffix.
	num := strings.TrimRight(token, "abBGiKkMmpst")
	unit := strings.ToLower(strings.TrimPrefix(token, num))
	val, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	var mbps float64
	switch {
	case strings.HasPrefix(unit, "g"): // Gbit / Gbps
		mbps = val * 1000
	case strings.HasPrefix(unit, "m"): // Mbit / Mbps
		mbps = val
	case strings.HasPrefix(unit, "k"): // Kbit / Kbps
		mbps = val / 1000
	default: // bare bit/s
		mbps = val / 1e6
	}
	return int(mbps + 0.5)
}

// CleanupVM removes all network limits and rules for a VM
func (bm *BandwidthManager) CleanupVM(vmID string) error {
	return bm.RemoveBandwidthLimit(vmID)
}
