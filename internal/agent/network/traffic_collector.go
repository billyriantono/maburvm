// Package network provides bandwidth usage collection for VM interfaces.
package network

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/maburvm/panel/internal/agent/libvirt"
)

// VMTrafficStats holds cumulative byte counters for a VM's network interface.
type VMTrafficStats struct {
	VMID          string
	InterfaceName string
	RxBytes       int64
	TxBytes       int64
}

// CollectAllVMTraffic reads traffic counters for all running VMs by querying
// /sys/class/net/<vnet>/statistics/rx_bytes and tx_bytes.
func CollectAllVMTraffic() ([]VMTrafficStats, error) {
	// Get all running VMs
	vms, err := libvirt.ListVMs()
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}

	var stats []VMTrafficStats
	for _, vm := range vms {
		if vm.Status != libvirt.VMStatusRunning {
			continue
		}

		iface, err := libvirt.GetVMInterfaceName(vm.UUID)
		if err != nil {
			// VM might not have a network interface, skip
			continue
		}

		rx, err := readSysNetStat(iface, "rx_bytes")
		if err != nil {
			continue
		}

		tx, err := readSysNetStat(iface, "tx_bytes")
		if err != nil {
			continue
		}

		stats = append(stats, VMTrafficStats{
			VMID:          vm.UUID,
			InterfaceName: iface,
			RxBytes:       rx,
			TxBytes:       tx,
		})
	}

	return stats, nil
}

// readSysNetStat reads a single statistic from /sys/class/net/<iface>/statistics/<stat>
func readSysNetStat(iface, stat string) (int64, error) {
	path := fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, stat)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s: %w", path, err)
	}

	val, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	return val, nil
}
