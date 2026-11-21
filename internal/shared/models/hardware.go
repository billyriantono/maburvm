package models

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Hardware is a VM's virtual hardware beyond its size: the CPU it sees, how its
// vCPUs map onto host cores, memory backing, firmware extras and what happens
// when it crashes. On a plan it is what VMs ordered on the plan get; on a VM it
// is what the VM was given. The zero value is every VM from before it existed.
type Hardware struct {
	// CPUModel is the guest CPU: "" is the host's own CPU (host-passthrough),
	// "host-model", or a named model such as "Haswell-noTSX".
	CPUModel string `json:"cpu_model,omitempty"`
	// Sockets and Threads shape the vCPUs; the cores per socket follow from
	// the vCPU count. 0 is 1. A count that does not divide falls back to one
	// socket of single-thread cores, so a resize never breaks the VM.
	Sockets int `json:"sockets,omitempty"`
	Threads int `json:"threads,omitempty"`
	// DedicatedCPU gives each vCPU a host core of its own, from the node's
	// pool of dedicated cores (PinnedCPUs on the VM).
	DedicatedCPU bool `json:"dedicated_cpu,omitempty"`
	// NUMA keeps a dedicated VM's memory on the NUMA nodes of its cores.
	NUMA bool `json:"numa,omitempty"`
	// Hugepages backs the VM's memory with the node's reserved huge pages.
	Hugepages bool `json:"hugepages,omitempty"`
	// SecureBoot and TPM: UEFI Secure Boot (UEFI images only) and a virtual
	// TPM 2.0. Set when the VM is created; never changed on an existing VM.
	SecureBoot bool `json:"secure_boot,omitempty"`
	TPM        bool `json:"tpm,omitempty"`
	// Watchdog gives the guest a watchdog that resets it when it hangs.
	Watchdog bool `json:"watchdog,omitempty"`
	// RestartOnCrash has the node start the VM again when it crashes, with a
	// growing pause between restarts and an alert when it keeps crashing.
	RestartOnCrash bool `json:"restart_on_crash,omitempty"`

	// VM only. Firmware is how the VM boots (FirmwareBIOS or FirmwareUEFI),
	// "" for BIOS. PinnedCPUs are the host cores a dedicated VM holds.
	Firmware   string `json:"firmware,omitempty"`
	PinnedCPUs string `json:"pinned_cpus,omitempty"`
}

// Agent features (GetNodeInfo) the hardware options need. FeatureVMHardware
// covers VMConfig.hardware, SetVMHardware and restart on crash.
const (
	FeatureSecureBoot      = "secure_boot"
	FeatureTPM             = "tpm"
	FeatureHugepages       = "hugepages"
	FeatureVMHardware      = "vm_hardware"
	FeatureSnapshotOptions = "snapshot_options"
	FeatureOfflinePassword = "offline_password"
	FeatureBuiltinRescue   = "builtin_rescue"
)

// Limits a hardware profile is accepted in.
const (
	MaxSockets = 8
	MaxThreads = 2
)

var cpuModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// IsZero reports a profile that asks for nothing beyond a plain VM.
func (h Hardware) IsZero() bool { return h == Hardware{} }

// Validate checks the profile. vcpus is the VM's vCPU count, or 0 for a plan
// where only the shape is checked.
func (h Hardware) Validate(vcpus int) error {
	if h.CPUModel != "" && !cpuModelRe.MatchString(h.CPUModel) {
		return fmt.Errorf("the CPU model must be a name such as host-model or Haswell-noTSX")
	}
	if h.Sockets < 0 || h.Sockets > MaxSockets {
		return fmt.Errorf("sockets must be between 1 and %d", MaxSockets)
	}
	if h.Threads < 0 || h.Threads > MaxThreads {
		return fmt.Errorf("threads per core must be 1 or %d", MaxThreads)
	}
	if h.NUMA && !h.DedicatedCPU {
		return fmt.Errorf("NUMA placement follows dedicated CPUs: turn on dedicated CPUs too")
	}
	if h.Firmware != "" && h.Firmware != FirmwareBIOS && h.Firmware != FirmwareUEFI {
		return fmt.Errorf("firmware must be bios or uefi")
	}
	if h.PinnedCPUs != "" {
		cpus, err := ParseCPUSet(h.PinnedCPUs)
		if err != nil {
			return err
		}
		if vcpus > 0 && len(cpus) != vcpus {
			return fmt.Errorf("a dedicated VM holds one host core per vCPU: %d cores for %d vCPUs", len(cpus), vcpus)
		}
	}
	return nil
}

// Topology is the sockets, cores and threads vcpus are laid out in. It falls
// back to one socket of single-thread cores when the shape does not divide.
func (h Hardware) Topology(vcpus int) (sockets, cores, threads int) {
	sockets, threads = max(h.Sockets, 1), max(h.Threads, 1)
	if vcpus <= 0 || vcpus%(sockets*threads) != 0 {
		return 1, max(vcpus, 1), 1
	}
	return sockets, vcpus / (sockets * threads), threads
}

// ParseCPUSet reads a host CPU list such as "2-5,8,10-11" into sorted,
// distinct CPU numbers.
func ParseCPUSet(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a < 0 {
			return nil, fmt.Errorf("%q is not a CPU list such as 2-5,8", s)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil || b < a {
				return nil, fmt.Errorf("%q is not a CPU list such as 2-5,8", s)
			}
		}
		if b > 4095 || b-a > 4095 {
			return nil, fmt.Errorf("CPU numbers in %q are out of range", s)
		}
		for c := a; c <= b; c++ {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// FormatCPUSet writes CPU numbers as a compact list ("2-5,8").
func FormatCPUSet(cpus []int) string {
	c := slices.Clone(cpus)
	slices.Sort(c)
	c = slices.Compact(c)
	var parts []string
	for i := 0; i < len(c); {
		j := i
		for j+1 < len(c) && c[j+1] == c[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d-%d", c[i], c[j]))
		} else {
			parts = append(parts, strconv.Itoa(c[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// FreeCPUs picks n cores from pool that no VM in used holds, lowest first;
// ok is false when the pool has fewer free.
func FreeCPUs(pool []int, used []string, n int) (picked []int, ok bool) {
	taken := map[int]bool{}
	for _, u := range used {
		cpus, _ := ParseCPUSet(u)
		for _, c := range cpus {
			taken[c] = true
		}
	}
	for _, c := range pool {
		if len(picked) == n {
			break
		}
		if !taken[c] {
			picked = append(picked, c)
		}
	}
	return picked, len(picked) == n
}
