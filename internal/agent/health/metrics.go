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

// counterRate turns two readings of a monotonic counter into a per-second rate.
//
// Returns 0 whenever the pair cannot be trusted rather than a number that
// merely looks like one:
//
//   - No previous reading (first sample, or the first after a restart): there is
//     no delta to take. Subtracting zero from a lifetime counter yields a rate
//     larger than any hardware can produce.
//   - The counter went backwards: interface counters reset, and these are
//     unsigned, so the subtraction would wrap to something astronomical instead
//     of going negative.
//   - No elapsed time: nothing can be divided by it.
//
// A missing sample costs one gap in a graph. A fabricated one sets the scale for
// every other point and hides them all.
func counterRate(previous, current uint64, havePrevious bool, intervalSeconds float64) int64 {
	if !havePrevious || intervalSeconds <= 0 || current < previous {
		return 0
	}
	return int64(float64(current-previous) / intervalSeconds)
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
		// Same fault as the network counters below: the guard never fired, so the
		// first sample after a restart published the disk's lifetime byte count
		// as an instantaneous rate.
		prevRead, haveRead := m.lastDiskStats["totalRead"]
		prevWrite, haveWrite := m.lastDiskStats["totalWrite"]
		metrics.DiskReadBytesPerSec = counterRate(prevRead, totalRead, haveRead, interval)
		metrics.DiskWriteBytesPerSec = counterRate(prevWrite, totalWrite, haveWrite, interval)
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
		// Presence in the map, not lastCollection.IsZero(): the constructor sets
		// lastCollection to now, so that guard never fired. The first sample
		// after an agent restart therefore subtracted zero from a counter that
		// had been accumulating since the node booted and published the node's
		// entire lifetime traffic as an instantaneous rate — 4.6e17 bytes/sec was
		// recorded on a live node, which is 460 exabytes per second. One such
		// sample sets the scale of every chart drawn from the series, flattening
		// every real measurement to the axis.
		prevRX, haveRX := m.lastNetStats["totalRX"]
		prevTX, haveTX := m.lastNetStats["totalTX"]
		metrics.NetworkRXBytesPerSec = counterRate(prevRX, totalRX, haveRX, interval)
		metrics.NetworkTXBytesPerSec = counterRate(prevTX, totalTX, haveTX, interval)
		m.lastNetStats["totalRX"] = totalRX
		m.lastNetStats["totalTX"] = totalTX
	}

	// Running VMs
	metrics.RunningVMCount = m.runningVMCount

	m.lastCollection = now

	return metrics
}
