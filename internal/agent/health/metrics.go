package health

import (
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// NodeMetrics holds the collected system metrics.
type NodeMetrics struct {
	CPUPercent           float64
	MemoryUsed           int64
	MemoryTotal          int64
	MemoryUsedPercent    float64
	DiskUsed             int64
	DiskTotal            int64
	DiskUsedPercent      float64
	NetworkRXBytesPerSec int64
	NetworkTXBytesPerSec int64
	DiskReadBytesPerSec  int64
	DiskWriteBytesPerSec int64
	RunningVMCount       int
	AvailableCPUs        int32
	AvailableMemoryMB    int64
	AvailableDiskGB      int64
	LoadAvg              []float64
}

// MetricsCollector collects system metrics.
type MetricsCollector struct {
	mu             sync.RWMutex
	lastNetStats   map[string]uint64
	lastDiskStats  map[string]uint64
	lastCollection time.Time
	runningVMCount int
	vmIDs          []string
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		lastNetStats:   make(map[string]uint64),
		lastDiskStats:  make(map[string]uint64),
		lastCollection: time.Now(),
	}
}

// SetRunningVMs sets the current running VM count and their IDs.
func (m *MetricsCollector) SetRunningVMs(count int, vmIDs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runningVMCount = count
	m.vmIDs = vmIDs
}

// GetRunningVMIDs returns the list of active VM IDs.
func (m *MetricsCollector) GetRunningVMIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to avoid race conditions
	result := make([]string, len(m.vmIDs))
	copy(result, m.vmIDs)
	return result
}

// Collect gathers current system metrics.
func (m *MetricsCollector) Collect() *NodeMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics := &NodeMetrics{}
	now := time.Now()
	interval := now.Sub(m.lastCollection).Seconds()
	if interval <= 0 {
		interval = 1
	}

	// CPU metrics
	percent, err := cpu.Percent(0, false)
	if err == nil && len(percent) > 0 {
		metrics.CPUPercent = percent[0]
	}
	metrics.AvailableCPUs = int32(runtime.NumCPU())

	// Load average
	loadAvg, err := load.Avg()
	if err == nil {
		metrics.LoadAvg = []float64{loadAvg.Load1, loadAvg.Load5, loadAvg.Load15}
	}

	// Memory metrics
	vMem, err := mem.VirtualMemory()
	if err == nil {
		metrics.MemoryUsed = int64(vMem.Used)
		metrics.MemoryTotal = int64(vMem.Total)
		metrics.MemoryUsedPercent = vMem.UsedPercent
		metrics.AvailableMemoryMB = int64(vMem.Available / 1024 / 1024)
	}

	// Disk metrics
	diskUsage, err := disk.Usage("/")
	if err == nil {
		metrics.DiskUsed = int64(diskUsage.Used)
		metrics.DiskTotal = int64(diskUsage.Total)
		metrics.DiskUsedPercent = diskUsage.UsedPercent
		metrics.AvailableDiskGB = int64(diskUsage.Free / 1024 / 1024 / 1024)
	}

	// Disk I/O counters
	ioCounters, err := disk.IOCounters()
	if err == nil {
		var totalRead, totalWrite uint64
		for _, counter := range ioCounters {
			totalRead += counter.ReadBytes
			totalWrite += counter.WriteBytes
		}
		// Calculate per-second rate
		if m.lastCollection.IsZero() {
			metrics.DiskReadBytesPerSec = 0
			metrics.DiskWriteBytesPerSec = 0
		} else {
			metrics.DiskReadBytesPerSec = int64(float64(totalRead-m.lastDiskStats["totalRead"]) / interval)
			metrics.DiskWriteBytesPerSec = int64(float64(totalWrite-m.lastDiskStats["totalWrite"]) / interval)
		}
		m.lastDiskStats["totalRead"] = totalRead
		m.lastDiskStats["totalWrite"] = totalWrite
	}

	// Network metrics
	netCounters, err := net.IOCounters(false)
	if err == nil && len(netCounters) > 0 {
		var totalRX, totalTX uint64
		for _, counter := range netCounters {
			totalRX += counter.BytesRecv
			totalTX += counter.BytesSent
		}
		// Calculate per-second rate
		if m.lastCollection.IsZero() {
			metrics.NetworkRXBytesPerSec = 0
			metrics.NetworkTXBytesPerSec = 0
		} else {
			metrics.NetworkRXBytesPerSec = int64(float64(totalRX-m.lastNetStats["totalRX"]) / interval)
			metrics.NetworkTXBytesPerSec = int64(float64(totalTX-m.lastNetStats["totalTX"]) / interval)
		}
		m.lastNetStats["totalRX"] = totalRX
		m.lastNetStats["totalTX"] = totalTX
	}

	// Running VMs
	metrics.RunningVMCount = m.runningVMCount

	m.lastCollection = now

	return metrics
}
