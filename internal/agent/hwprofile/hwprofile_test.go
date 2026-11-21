package hwprofile

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirtxml"
)

func newDomain(machine string, vcpus uint) *libvirtxml.Domain {
	return &libvirtxml.Domain{
		Name: "vm1",
		VCPU: &libvirtxml.DomainVCPU{Value: vcpus},
		OS:   &libvirtxml.DomainOS{Type: &libvirtxml.DomainOSType{Arch: "x86_64", Machine: machine, Type: "hvm"}},
		CPU:  CPU(""),
		Metadata: &libvirtxml.DomainMetadata{XML: `<maburvm:metadata xmlns:maburvm="http://maburvm.local/schema">` +
			`<maburvm:os_type>linux</maburvm:os_type></maburvm:metadata>`},
		Devices: &libvirtxml.DomainDeviceList{},
	}
}

func marshal(t *testing.T, d *libvirtxml.Domain) string {
	t.Helper()
	out, err := xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestFindFirmwarePrefersNewestLayout(t *testing.T) {
	have := map[string]bool{
		"/usr/share/OVMF/OVMF_CODE_4M.fd": true, "/usr/share/OVMF/OVMF_VARS_4M.fd": true,
		"/usr/share/OVMF/OVMF_CODE.fd": true, "/usr/share/OVMF/OVMF_VARS.fd": true,
		// Secure Boot code without its key store is no use.
		"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd": true,
	}
	plain, secure := FindFirmware(func(p string) bool { return have[p] })
	if plain == nil || plain.Code != "/usr/share/OVMF/OVMF_CODE_4M.fd" || secure != nil {
		t.Fatalf("plain %+v secure %+v", plain, secure)
	}
	have["/usr/share/OVMF/OVMF_VARS_4M.ms.fd"] = true
	if _, secure = FindFirmware(func(p string) bool { return have[p] }); secure == nil || !secure.Secure {
		t.Fatalf("secure firmware not found: %+v", secure)
	}
	if p, s := FindFirmware(func(string) bool { return false }); p != nil || s != nil {
		t.Fatal("firmware found on a host without OVMF")
	}
}

func TestSecureBootFirmwareNeedsQ35AndTurnsOnSMM(t *testing.T) {
	fw := &Firmware{Code: "/c", Vars: "/v", Secure: true}
	if err := SetFirmware(newDomain("pc", 1), fw); err == nil {
		t.Fatal("Secure Boot accepted on the pc machine")
	}
	d := newDomain("q35", 1)
	if err := SetFirmware(d, fw); err != nil {
		t.Fatal(err)
	}
	x := marshal(t, d)
	for _, want := range []string{`<loader readonly="yes" secure="yes" type="pflash">/c</loader>`,
		`<nvram template="/v">/var/lib/libvirt/qemu/nvram/vm1_VARS.fd</nvram>`, `<smm state="on">`} {
		if !strings.Contains(x, want) {
			t.Fatalf("missing %s in %s", want, x)
		}
	}
	if !UsesUEFI(d) {
		t.Fatal("UEFI domain not recognised")
	}
}

func TestLocalFirmwareForAMovedVM(t *testing.T) {
	d := newDomain("q35", 1)
	if err := SetFirmware(d, &Firmware{Code: "/elsewhere/OVMF_CODE.secboot.fd", Vars: "/elsewhere/OVMF_VARS.ms.fd", Secure: true}); err != nil {
		t.Fatal(err)
	}
	moved := marshal(t, d)
	here := map[string]bool{"/usr/share/OVMF/OVMF_CODE_4M.secboot.fd": true, "/usr/share/OVMF/OVMF_VARS_4M.ms.fd": true}
	out, err := LocalFirmware(moved, func(p string) bool { return here[p] })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`secure="yes" type="pflash">/usr/share/OVMF/OVMF_CODE_4M.secboot.fd</loader>`,
		`<nvram template="/usr/share/OVMF/OVMF_VARS_4M.ms.fd">/var/lib/libvirt/qemu/nvram/vm1_VARS.fd</nvram>`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	if _, err := LocalFirmware(moved, func(string) bool { return false }); err == nil {
		t.Fatal("a UEFI VM was defined on a node without UEFI firmware")
	}
	// Firmware found where the domain says, or no firmware at all: untouched.
	if out, _ := LocalFirmware(moved, func(string) bool { return true }); out != moved {
		t.Fatal("a domain whose firmware is here was rewritten")
	}
	bios := marshal(t, newDomain("pc", 1))
	if out, _ := LocalFirmware(bios, func(string) bool { return false }); out != bios {
		t.Fatal("a BIOS domain was rewritten")
	}
}

func TestTPMModelFollowsMachine(t *testing.T) {
	for _, c := range []struct{ machine, model string }{{"q35", "tpm-crb"}, {"pc", "tpm-tis"}} {
		d := newDomain(c.machine, 1)
		SetTPM(d)
		if x := marshal(t, d); !strings.Contains(x, `<tpm model="`+c.model+`"><backend type="emulator" version="2.0">`) {
			t.Fatalf("%s: %s", c.machine, x)
		}
	}
}

func TestPinningAndNUMA(t *testing.T) {
	d := newDomain("q35", 2)
	d.CPUTune = &libvirtxml.DomainCPUTune{Shares: &libvirtxml.DomainCPUTuneShares{Value: 200}}
	node := func(cpu int) int { return cpu / 4 }
	SetPinning(d, []int{4, 5}, true, node)
	x := marshal(t, d)
	for _, want := range []string{`<vcpupin vcpu="0" cpuset="4">`, `<vcpupin vcpu="1" cpuset="5">`, `<emulatorpin cpuset="4-5">`,
		`<shares>200</shares>`, `<memory mode="preferred" nodeset="1">`} {
		if !strings.Contains(x, want) {
			t.Fatalf("missing %s in %s", want, x)
		}
	}
	SetPinning(d, []int{3, 4}, true, node)
	if !strings.Contains(marshal(t, d), `mode="interleave" nodeset="0-1"`) {
		t.Fatal("cores on two nodes did not interleave")
	}
	SetPinning(d, nil, false, nil)
	x = marshal(t, d)
	if strings.Contains(x, "vcpupin") || strings.Contains(x, "numatune") || !strings.Contains(x, "<shares>200</shares>") {
		t.Fatalf("unpinning left %s", x)
	}
	d.VCPU.Value = 1
	SetPinning(d, []int{4, 5}, false, nil)
	TrimPinning(d)
	if strings.Contains(marshal(t, d), `vcpu="1"`) {
		t.Fatal("pin of a removed vCPU kept")
	}
}

func TestTopologyAndCPUModel(t *testing.T) {
	d := newDomain("q35", 4)
	if err := ApplyMutable(d, models.Hardware{CPUModel: "Haswell-noTSX", Sockets: 2, Threads: 2}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	x := marshal(t, d)
	if !strings.Contains(x, `<model fallback="allow">Haswell-noTSX</model>`) || !strings.Contains(x, `<topology sockets="2" cores="1" threads="2">`) {
		t.Fatal(x)
	}
	// Changing only the model keeps the shape.
	if err := ApplyMutable(d, models.Hardware{CPUModel: "host-model"}, nil, []string{"cpu_model"}, nil); err != nil {
		t.Fatal(err)
	}
	if x = marshal(t, d); !strings.Contains(x, `mode="host-model"`) || !strings.Contains(x, `sockets="2"`) {
		t.Fatal(x)
	}
	if err := ApplyMutable(d, models.Hardware{}, nil, []string{"firmware"}, nil); err == nil {
		t.Fatal("firmware changed on an existing VM")
	}
	SetTopology(d, models.Hardware{})
	if strings.Contains(marshal(t, d), "topology") {
		t.Fatal("plain shape kept a topology")
	}
}

func TestFitVCPUsKeepsTheShape(t *testing.T) {
	if FitVCPUs(newDomain("q35", 2), 4, nil, nil) {
		t.Fatal("a plain VM's definition was rewritten for a resize")
	}
	// An imported VM's one socket stays one socket: dropping the element would
	// give it a socket per vCPU.
	d := newDomain("q35", 2)
	d.CPU.Topology = &libvirtxml.DomainCPUTopology{Sockets: 1, Dies: 1, Cores: 2, Threads: 1}
	if !FitVCPUs(d, 4, nil, nil) || d.VCPU.Value != 4 {
		t.Fatal("topology not fitted")
	}
	if x := marshal(t, d); !strings.Contains(x, `<topology sockets="1" cores="4" threads="1">`) {
		t.Fatal(x)
	}
	d.CPU.Topology = &libvirtxml.DomainCPUTopology{Sockets: 2, Cores: 2, Threads: 1}
	FitVCPUs(d, 3, nil, nil)
	if x := marshal(t, d); !strings.Contains(x, `<topology sockets="1" cores="3" threads="1">`) {
		t.Fatal("a shape that does not divide must fall back to one socket: " + x)
	}
}

func TestWatchdogAndRestartOnCrash(t *testing.T) {
	pc := newDomain("pc", 1)
	SetWatchdog(pc, true)
	if !strings.Contains(marshal(t, pc), `<watchdog model="i6300esb" action="reset">`) {
		t.Fatal("pc machine got no watchdog")
	}
	SetWatchdog(pc, false)
	if pc.Devices.Watchdog != nil {
		t.Fatal("watchdog not removed")
	}
	q35 := newDomain("q35", 1)
	SetWatchdog(q35, true)
	if q35.Devices.Watchdog != nil || !HasWatchdog(q35) {
		t.Fatal("q35 got a second watchdog beside its chipset one")
	}

	SetRestartOnCrash(q35, true)
	x := marshal(t, q35)
	if !RestartOnCrash(x) || !strings.Contains(x, `<panic model="isa">`) || !strings.Contains(x, "<maburvm:os_type>linux</maburvm:os_type>") {
		t.Fatal(x)
	}
	SetRestartOnCrash(q35, false)
	if x = marshal(t, q35); RestartOnCrash(x) || strings.Contains(x, "panic") {
		t.Fatal(x)
	}
	bare := &libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{}}
	SetRestartOnCrash(bare, true)
	if !RestartOnCrash(marshal(t, bare)) {
		t.Fatal("marker not added to a domain without metadata")
	}
}

func TestHugepages(t *testing.T) {
	d := newDomain("q35", 1)
	SetHugepages(d, true)
	if !strings.Contains(marshal(t, d), "<hugepages>") {
		t.Fatal("no hugepages")
	}
	SetHugepages(d, false)
	if d.MemoryBacking != nil {
		t.Fatal("empty memory backing kept")
	}
}

func TestHugepagesFromMeminfo(t *testing.T) {
	total, free, kib := Hugepages("MemTotal: 65000000 kB\nHugePages_Total:    1024\nHugePages_Free:      512\nHugepagesize:       2048 kB\n")
	if total != 1024 || free != 512 || kib != 2048 {
		t.Fatalf("%d %d %d", total, free, kib)
	}
}
