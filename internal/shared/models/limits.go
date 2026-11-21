package models

import "fmt"

// VMLimits caps what one VM may take from its node, so a busy guest cannot
// starve its neighbours. Zero in a field means no limit; the zero value is what
// every VM had before limits existed.
type VMLimits struct {
	// CPUCapPercent caps each vCPU at this share of one host core (1-100).
	CPUCapPercent int `json:"cpu_cap_percent"`
	// CPUWeight is the VM's CPU priority against the others on its node when
	// the node is busy: 100 is normal, 50 half, 200 double. 0 leaves the
	// node's default.
	CPUWeight int `json:"cpu_weight"`
	// Disk operations per second: a total, or separate read and write limits.
	IOPS      int `json:"iops"`
	ReadIOPS  int `json:"read_iops"`
	WriteIOPS int `json:"write_iops"`
	// Disk throughput in MB/s (1 MB = 1,000,000 bytes): a total, or separate
	// read and write limits.
	MBps      int `json:"mbps"`
	ReadMBps  int `json:"read_mbps"`
	WriteMBps int `json:"write_mbps"`
}

// Ranges a limit is accepted in.
const (
	MaxCPUWeight = 1000
	MinIOPSLimit = 10
	MaxIOPSLimit = 1000000
	MaxMBpsLimit = 100000
)

// IsZero reports a VM with no limits at all.
func (l VMLimits) IsZero() bool { return l == VMLimits{} }

// Validate checks every field is in range and that no total is set alongside
// a read or write limit of the same kind, which libvirt refuses.
func (l VMLimits) Validate() error {
	if l.CPUCapPercent < 0 || l.CPUCapPercent > 100 {
		return fmt.Errorf("the CPU cap must be between 1 and 100 percent, or 0 for none")
	}
	if l.CPUWeight < 0 || l.CPUWeight > MaxCPUWeight {
		return fmt.Errorf("the CPU priority must be between 1 and %d, or 0 for the node's default", MaxCPUWeight)
	}
	for _, v := range []int{l.IOPS, l.ReadIOPS, l.WriteIOPS} {
		if v != 0 && (v < MinIOPSLimit || v > MaxIOPSLimit) {
			return fmt.Errorf("a disk IOPS limit must be between %d and %d, or 0 for none", MinIOPSLimit, MaxIOPSLimit)
		}
	}
	for _, v := range []int{l.MBps, l.ReadMBps, l.WriteMBps} {
		if v < 0 || v > MaxMBpsLimit {
			return fmt.Errorf("a disk throughput limit must be between 1 and %d MB/s, or 0 for none", MaxMBpsLimit)
		}
	}
	if l.IOPS > 0 && (l.ReadIOPS > 0 || l.WriteIOPS > 0) {
		return fmt.Errorf("set either a total IOPS limit or separate read and write limits, not both")
	}
	if l.MBps > 0 && (l.ReadMBps > 0 || l.WriteMBps > 0) {
		return fmt.Errorf("set either a total throughput limit or separate read and write limits, not both")
	}
	return nil
}

// WithThrottle is what a VM runs under while an operator's throttle is on: the
// tighter of the two for every cap. A throttle's CPU priority replaces the
// VM's own. Against separate read and write limits a throttle's total caps
// each direction, so the result never mixes a total with a split.
func (l VMLimits) WithThrottle(t *VMLimits) VMLimits {
	if t == nil {
		return l
	}
	out := l
	out.CPUCapPercent = tighter(l.CPUCapPercent, t.CPUCapPercent)
	if t.CPUWeight > 0 {
		out.CPUWeight = t.CPUWeight
	}
	out.IOPS, out.ReadIOPS, out.WriteIOPS = tighterSplit(l.IOPS, l.ReadIOPS, l.WriteIOPS, t.IOPS)
	out.MBps, out.ReadMBps, out.WriteMBps = tighterSplit(l.MBps, l.ReadMBps, l.WriteMBps, t.MBps)
	return out
}

// tighter is the smaller of two limits where 0 means none.
func tighter(a, b int) int {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

func tighterSplit(total, read, write, cap int) (int, int, int) {
	if cap == 0 {
		return total, read, write
	}
	if read == 0 && write == 0 {
		return tighter(total, cap), 0, 0
	}
	return 0, tighter(read, cap), tighter(write, cap)
}
