// Package hwprofile writes a VM's virtual hardware profile (models.Hardware)
// into its libvirt definition: CPU model and topology, dedicated cores and
// NUMA placement, huge pages, UEFI firmware and Secure Boot, a TPM, a
// watchdog and the restart-on-crash marker. It uses no libvirt bindings, so
// it builds and tests anywhere.
package hwprofile

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirtxml"
)

// NVRAMDir is where libvirt keeps each UEFI VM's copy of the variable store.
const NVRAMDir = "/var/lib/libvirt/qemu/nvram"

// Firmware is a UEFI code image and the variable store new VMs start from.
type Firmware struct {
	Code, Vars string
	// Secure is Secure Boot firmware: it needs SMM, so a q35 machine, and its
	// variable store has the usual keys enrolled.
	Secure bool
}

// Where distributions put OVMF, newest layout first.
var (
	plainFirmware = []Firmware{
		{Code: "/usr/share/OVMF/OVMF_CODE_4M.fd", Vars: "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{Code: "/usr/share/OVMF/OVMF_CODE.fd", Vars: "/usr/share/OVMF/OVMF_VARS.fd"},
		{Code: "/usr/share/edk2/ovmf/OVMF_CODE.fd", Vars: "/usr/share/edk2/ovmf/OVMF_VARS.fd"},
		{Code: "/usr/share/edk2/x64/OVMF_CODE.4m.fd", Vars: "/usr/share/edk2/x64/OVMF_VARS.4m.fd"},
	}
	secureFirmware = []Firmware{
		{Code: "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd", Vars: "/usr/share/OVMF/OVMF_VARS_4M.ms.fd", Secure: true},
		{Code: "/usr/share/OVMF/OVMF_CODE.secboot.fd", Vars: "/usr/share/OVMF/OVMF_VARS.ms.fd", Secure: true},
		{Code: "/usr/share/edk2/ovmf/OVMF_CODE.secboot.fd", Vars: "/usr/share/edk2/ovmf/OVMF_VARS.secboot.fd", Secure: true},
	}
)

// FindFirmware returns the UEFI firmware the host has: plain, and with Secure
// Boot. Either is nil when missing.
func FindFirmware(exists func(string) bool) (plain, secure *Firmware) {
	pick := func(list []Firmware) *Firmware {
		for i := range list {
			if exists(list[i].Code) && exists(list[i].Vars) {
				f := list[i]
				return &f
			}
		}
		return nil
	}
	return pick(plainFirmware), pick(secureFirmware)
}

// CPU is the <cpu> element for a model: "" is the host's own CPU
// (host-passthrough), "host-model" and "host-passthrough" are modes, and any
// other value is a named model.
func CPU(model string) *libvirtxml.DomainCPU {
	switch model {
	case "", "host-passthrough":
		// Not an old baseline model: kvm64/qemu64 lack x86-64-v2 (SSE4.2,
		// AVX...), and current software stops on them (MongoDB 7, mysql:9).
		// The cost: such a VM moves live only to a node with the same CPU.
		return &libvirtxml.DomainCPU{Mode: "host-passthrough"}
	case "host-model":
		return &libvirtxml.DomainCPU{Mode: "host-model"}
	}
	return &libvirtxml.DomainCPU{
		Mode:  "custom",
		Match: "exact",
		Model: &libvirtxml.DomainCPUModel{Fallback: "allow", Value: model},
	}
}

// SetCPU gives the domain its CPU model and lays its vCPUs out as hw says.
func SetCPU(dom *libvirtxml.Domain, hw models.Hardware) {
	dom.CPU = CPU(hw.CPUModel)
	SetTopology(dom, hw)
}

// SetTopology lays the domain's vCPUs out in hw's sockets and threads,
// keeping its CPU model. A plain one-socket single-thread shape writes no
// topology, as before profiles existed.
func SetTopology(dom *libvirtxml.Domain, hw models.Hardware) {
	if dom.CPU == nil {
		dom.CPU = CPU("")
	}
	vcpus := 1
	if dom.VCPU != nil && dom.VCPU.Value > 0 {
		vcpus = int(dom.VCPU.Value)
	}
	sockets, cores, threads := hw.Topology(vcpus)
	if sockets == 1 && threads == 1 && max(hw.Sockets, 1) == 1 && max(hw.Threads, 1) == 1 {
		dom.CPU.Topology = nil
		return
	}
	dom.CPU.Topology = &libvirtxml.DomainCPUTopology{Sockets: sockets, Cores: cores, Threads: threads}
}

// SetPinning gives vCPU i host core cpus[i] and runs the emulator threads on
// the same cores; nil cpus removes the pinning. With numa the memory goes on
// the NUMA nodes of those cores (numaNode answers -1 for unknown). The
// domain's CPU shares and quota are kept.
func SetPinning(dom *libvirtxml.Domain, cpus []int, numa bool, numaNode func(cpu int) int) {
	if dom.CPUTune != nil {
		dom.CPUTune.VCPUPin, dom.CPUTune.EmulatorPin = nil, nil
	}
	dom.NUMATune = nil
	if len(cpus) == 0 {
		return
	}
	if dom.CPUTune == nil {
		dom.CPUTune = &libvirtxml.DomainCPUTune{}
	}
	for i, c := range cpus {
		dom.CPUTune.VCPUPin = append(dom.CPUTune.VCPUPin, libvirtxml.DomainCPUTuneVCPUPin{VCPU: uint(i), CPUSet: strconv.Itoa(c)})
	}
	dom.CPUTune.EmulatorPin = &libvirtxml.DomainCPUTuneEmulatorPin{CPUSet: models.FormatCPUSet(cpus)}
	if !numa || numaNode == nil {
		return
	}
	var nodes []int
	for _, c := range cpus {
		if n := numaNode(c); n >= 0 {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return
	}
	set := models.FormatCPUSet(nodes)
	// One node: memory there while it has room. Several: spread over them.
	// Never strict, which kills the guest when its node runs out.
	mode := "preferred"
	if strings.ContainsAny(set, ",-") {
		mode = "interleave"
	}
	dom.NUMATune = &libvirtxml.DomainNUMATune{Memory: &libvirtxml.DomainNUMATuneMemory{Mode: mode, Nodeset: set}}
}

// TrimPinning drops the pins of vCPUs the domain no longer has, after a
// resize down.
func TrimPinning(dom *libvirtxml.Domain) {
	if dom.CPUTune == nil || dom.VCPU == nil {
		return
	}
	kept := dom.CPUTune.VCPUPin[:0]
	for _, p := range dom.CPUTune.VCPUPin {
		if p.VCPU < dom.VCPU.Value {
			kept = append(kept, p)
		}
	}
	dom.CPUTune.VCPUPin = kept
}

// FitVCPUs sets a saved definition's vCPU count, keeping its topology's shape
// and pinning in step: libvirt refuses a vCPU count its topology does not
// hold. cpus, when set, are the cores a dedicated VM holds after the change.
// It reports whether the definition has a topology or pinning to keep in step.
func FitVCPUs(def *libvirtxml.Domain, vcpus int, cpus []int, numaNode func(int) int) bool {
	shaped := def.CPU != nil && def.CPU.Topology != nil
	pinned := def.CPUTune != nil && len(def.CPUTune.VCPUPin) > 0
	if !shaped && !pinned && len(cpus) == 0 {
		return false
	}
	if def.VCPU == nil {
		def.VCPU = &libvirtxml.DomainVCPU{}
	}
	def.VCPU.Value, def.VCPU.Current = uint(vcpus), 0
	if shaped {
		// Written out even for one socket: without a topology QEMU gives each
		// vCPU a socket of its own, and Windows desktop editions use two sockets.
		t := def.CPU.Topology
		sockets, cores, threads := models.Hardware{Sockets: t.Sockets, Threads: t.Threads}.Topology(vcpus)
		def.CPU.Topology = &libvirtxml.DomainCPUTopology{Sockets: sockets, Cores: cores, Threads: threads}
	}
	if len(cpus) > 0 {
		SetPinning(def, cpus, def.NUMATune != nil, numaNode)
	} else {
		TrimPinning(def)
	}
	return true
}

// SetHugepages backs the domain's memory with the host's huge pages, or not.
func SetHugepages(dom *libvirtxml.Domain, on bool) {
	if on {
		if dom.MemoryBacking == nil {
			dom.MemoryBacking = &libvirtxml.DomainMemoryBacking{}
		}
		dom.MemoryBacking.MemoryHugePages = &libvirtxml.DomainMemoryHugepages{}
		return
	}
	if dom.MemoryBacking != nil {
		dom.MemoryBacking.MemoryHugePages = nil
		if *dom.MemoryBacking == (libvirtxml.DomainMemoryBacking{}) {
			dom.MemoryBacking = nil
		}
	}
}

// machine is the domain's machine type.
func machine(dom *libvirtxml.Domain) string {
	if dom.OS == nil || dom.OS.Type == nil {
		return ""
	}
	return dom.OS.Type.Machine
}

func isQ35(dom *libvirtxml.Domain) bool { return strings.Contains(machine(dom), "q35") }

// SetWatchdog gives the guest a watchdog that resets it when it stops
// answering. A q35 machine has one in its chipset (iTCO), which resets by
// default and which newer libvirt lists by itself; a second device there would
// be dropped by every later edit of the definition, so only the older pc
// machine gets an i6300esb. Off removes only the i6300esb.
func SetWatchdog(dom *libvirtxml.Domain, on bool) {
	if dom.Devices == nil {
		return
	}
	w := dom.Devices.Watchdog
	switch {
	case on && !isQ35(dom):
		dom.Devices.Watchdog = &libvirtxml.DomainWatchdog{Model: "i6300esb", Action: "reset"}
	case on && w != nil && w.Model == "itco":
		w.Action = "reset"
	case !on && w != nil && w.Model == "i6300esb":
		dom.Devices.Watchdog = nil
	}
}

// HasWatchdog reports a guest whose watchdog resets it: q35 always does.
func HasWatchdog(dom *libvirtxml.Domain) bool {
	return isQ35(dom) || (dom.Devices != nil && dom.Devices.Watchdog != nil)
}

const restartElement = "restart_on_crash"

// SetRestartOnCrash marks the domain for the node's restart-on-crash policy
// and gives it a panic device, so a guest kernel panic stops the domain as
// crashed (on_crash destroy) for the node to start again. Off removes both.
func SetRestartOnCrash(dom *libvirtxml.Domain, on bool) {
	value := ""
	if on {
		value = "yes"
	}
	setMetadata(dom, restartElement, value)
	if dom.Devices == nil {
		return
	}
	panics := dom.Devices.Panics[:0]
	for _, p := range dom.Devices.Panics {
		if p.Model != "isa" {
			panics = append(panics, p)
		}
	}
	if on {
		panics = append(panics, libvirtxml.DomainPanic{Model: "isa"})
	}
	dom.Devices.Panics = panics
	if len(dom.Devices.Panics) == 0 {
		dom.Devices.Panics = nil
	}
}

var restartRe = regexp.MustCompile(`<maburvm:restart_on_crash>yes</maburvm:restart_on_crash>`)

// RestartOnCrash reports a domain definition marked for restart on crash.
func RestartOnCrash(domXML string) bool { return restartRe.MatchString(domXML) }

// SetFirmware boots the domain with UEFI from fw, its variable store copied
// for it under NVRAMDir when it first starts. Secure Boot firmware also turns
// on SMM. Only for a new domain: an installed system does not survive a change
// of firmware.
func SetFirmware(dom *libvirtxml.Domain, fw *Firmware) error {
	if fw == nil {
		return nil
	}
	if dom.OS == nil {
		return fmt.Errorf("the domain has no boot section")
	}
	if fw.Secure && !isQ35(dom) {
		return fmt.Errorf("Secure Boot needs the q35 machine type, and this kind of system uses %s", machine(dom))
	}
	dom.OS.Loader = &libvirtxml.DomainLoader{Path: fw.Code, Readonly: "yes", Type: "pflash"}
	dom.OS.NVRam = &libvirtxml.DomainNVRam{Template: fw.Vars, NVRam: NVRAMDir + "/" + dom.Name + "_VARS.fd"}
	if fw.Secure {
		dom.OS.Loader.Secure = "yes"
		if dom.Features == nil {
			dom.Features = &libvirtxml.DomainFeatureList{}
		}
		dom.Features.SMM = &libvirtxml.DomainFeatureSMM{State: "on"}
	}
	return nil
}

// LocalFirmware points a domain that arrived from another node at this host's
// UEFI firmware when the files it names are not here (distributions keep OVMF
// in different places); without that it would never start here. A domain
// whose firmware is here, or that has none, comes back unchanged.
func LocalFirmware(domXML string, exists func(string) bool) (string, error) {
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return "", fmt.Errorf("failed to parse the domain: %w", err)
	}
	if dom.OS == nil || dom.OS.Loader == nil || dom.OS.Loader.Path == "" || exists(dom.OS.Loader.Path) {
		return domXML, nil
	}
	plain, secure := FindFirmware(exists)
	fw := plain
	if dom.OS.Loader.Secure == "yes" {
		fw = secure
	}
	if fw == nil {
		return "", fmt.Errorf("the VM boots with UEFI firmware %s, which this node does not have; install the ovmf package here", dom.OS.Loader.Path)
	}
	dom.OS.Loader.Path = fw.Code
	if dom.OS.NVRam != nil {
		dom.OS.NVRam.Template = fw.Vars
	}
	return dom.Marshal()
}

// UsesUEFI reports a domain that boots with UEFI firmware.
func UsesUEFI(dom *libvirtxml.Domain) bool {
	return dom.OS != nil && (dom.OS.Loader != nil && dom.OS.Loader.Type == "pflash" || dom.OS.Firmware == "efi")
}

// SetTPM gives the domain an emulated TPM 2.0 (swtpm), the interface its
// machine type expects. Only for a new domain.
func SetTPM(dom *libvirtxml.Domain) {
	if dom.Devices == nil {
		return
	}
	model := "tpm-tis"
	if isQ35(dom) {
		model = "tpm-crb"
	}
	dom.Devices.TPMs = []libvirtxml.DomainTPM{{
		Model:   model,
		Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}},
	}}
}

func metadataTagRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`<maburvm:` + name + `>[^<]*</maburvm:` + name + `>`)
}

// setMetadata sets one element of the domain's maburvm metadata; "" removes it.
func setMetadata(dom *libvirtxml.Domain, name, value string) {
	el := ""
	if value != "" {
		el = "<maburvm:" + name + ">" + value + "</maburvm:" + name + ">"
	}
	re := metadataTagRe(name)
	switch {
	case dom.Metadata != nil && re.MatchString(dom.Metadata.XML):
		dom.Metadata.XML = re.ReplaceAllString(dom.Metadata.XML, el)
	case el == "":
	case dom.Metadata != nil && strings.Contains(dom.Metadata.XML, "</maburvm:metadata>"):
		dom.Metadata.XML = strings.Replace(dom.Metadata.XML, "</maburvm:metadata>", el+"</maburvm:metadata>", 1)
	default:
		if dom.Metadata == nil {
			dom.Metadata = &libvirtxml.DomainMetadata{}
		}
		dom.Metadata.XML += `<maburvm:metadata xmlns:maburvm="http://maburvm.local/schema">` + el + `</maburvm:metadata>`
	}
}

// Mutable lists the parts of a profile that can change on an existing VM, in
// the order SetVMHardware names them.
var Mutable = []string{"cpu_model", "topology", "pinning", "hugepages", "watchdog", "restart_on_crash"}

// ApplyMutable sets the named parts of hw on the domain (all of Mutable when
// fields is empty). cpus are the host cores of a dedicated VM.
func ApplyMutable(dom *libvirtxml.Domain, hw models.Hardware, cpus []int, fields []string, numaNode func(int) int) error {
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	all := len(fields) == 0
	for _, f := range fields {
		if !slices.Contains(Mutable, f) {
			return fmt.Errorf("%q is not a part of the hardware that can change", f)
		}
	}
	if all || want["cpu_model"] {
		topology := dom.CPU
		dom.CPU = CPU(hw.CPUModel)
		if topology != nil {
			dom.CPU.Topology = topology.Topology
		}
	}
	if all || want["topology"] {
		SetTopology(dom, hw)
	}
	if all || want["pinning"] {
		SetPinning(dom, cpus, hw.NUMA, numaNode)
	}
	if all || want["hugepages"] {
		SetHugepages(dom, hw.Hugepages)
	}
	if all || want["watchdog"] {
		SetWatchdog(dom, hw.Watchdog)
	}
	if all || want["restart_on_crash"] {
		SetRestartOnCrash(dom, hw.RestartOnCrash)
	}
	return nil
}
