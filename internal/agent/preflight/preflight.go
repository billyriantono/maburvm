// Package preflight checks that a host is set up the way the agent expects:
// KVM, libvirt, qemu-img, bridges, storage pool directories, bridge netfilter,
// the tools the agent runs and the clock. Gather observes the host once and
// Evaluate judges what it saw, so the judging is testable anywhere.
package preflight

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maburvm/panel/internal/agent/netrules"
)

// Check states, worst last.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
)

// Check is one line of the report.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Facts is what Gather saw on the host.
type Facts struct {
	KVMDevice bool
	// CPUVirt is "vmx" (Intel VT-x), "svm" (AMD-V) or "" when the CPU offers neither.
	CPUVirt string

	Virsh          bool
	LibvirtVersion string // the running daemon's; "" when it did not answer
	QEMUVersion    string
	LibvirtErr     string

	QemuImg string // qemu-img's version; "" when it is not installed
	// QemuImgFormats is nil when qemu-img did not list its formats.
	QemuImgFormats []string

	Bridges []BridgeFacts
	Pools   []PoolFacts
	// VLANBridges are the bridges named for per-VM VLANs; DHCP is, per bridge
	// served, why it is not ("" when it is).
	VLANBridges []netrules.VLANBridgeState
	DHCP        map[string]string

	BridgeNF  string // netrules.FirewallEnforced or netrules.FirewallOff
	IPForward bool
	Tools     map[string]bool // keyed by binary name
	// TimeSynced is "yes", "no", or "" when it could not be told.
	TimeSynced string

	// Optional hardware for VMs: UEFI firmware, Secure Boot firmware, the
	// TPM emulator, and the reserved huge page pool in MiB.
	UEFI, SecureBoot, TPM bool
	HugepagesMiB          int64
}

// BridgeFacts is one bridge the panel attaches VMs to.
type BridgeFacts struct {
	Name     string
	Exists   bool
	IsBridge bool
	Up       bool
	Ports    int
}

// PoolFacts is one storage pool directory.
type PoolFacts struct {
	Path string
	// Default marks the node's default image directory, checked because the
	// panel has no pool on the node, so new disks go there.
	Default    bool
	Exists     bool
	IsDir      bool
	Writable   bool
	Total      int64
	Available  int64
	MountPoint string
}

const gib = 1 << 30

// poolHeadroom mirrors the panel's admission: a new disk needs its own size
// plus this much free in its pool.
const poolHeadroom = 20 * gib

// Evaluate turns what was seen into the report.
func Evaluate(f Facts) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	if f.CPUVirt != "" {
		add(pass("cpu_virtualization", "CPU virtualization", fmt.Sprintf("The CPU offers hardware virtualization (%s).", f.CPUVirt)))
	} else {
		add(Check{ID: "cpu_virtualization", Name: "CPU virtualization", Status: Fail,
			Detail: "The CPU offers no hardware virtualization (no vmx or svm flag), so guests cannot use KVM.",
			Fix:    "Turn on Intel VT-x or AMD-V in the server's firmware settings. On a virtual machine used as a node, enable nested virtualization on its host."})
	}

	if f.KVMDevice {
		add(pass("kvm_device", "KVM device", "/dev/kvm is present."))
	} else {
		add(Check{ID: "kvm_device", Name: "KVM device", Status: Fail,
			Detail: "/dev/kvm is missing: the kvm module is not loaded.",
			Fix:    "Run modprobe kvm_intel (or kvm_amd) and add it to /etc/modules-load.d. If the CPU check fails too, fix that first."})
	}

	switch {
	case !f.Virsh:
		add(Check{ID: "libvirt", Name: "libvirt", Status: Fail,
			Detail: "virsh is not installed, so libvirt is most likely missing.",
			Fix:    "Install libvirt-daemon-system and libvirt-clients."})
	case f.LibvirtVersion == "":
		detail := "The libvirt daemon did not answer."
		if f.LibvirtErr != "" {
			detail = "The libvirt daemon did not answer: " + f.LibvirtErr
		}
		add(Check{ID: "libvirt", Name: "libvirt", Status: Fail, Detail: detail,
			Fix: "Start it: systemctl enable --now libvirtd (virtqemud on hosts with modular daemons)."})
	default:
		detail := "libvirt " + f.LibvirtVersion
		if f.QEMUVersion != "" {
			detail += ", QEMU " + f.QEMUVersion
		}
		add(pass("libvirt", "libvirt", detail+"."))
	}

	switch {
	case f.QemuImg == "":
		add(Check{ID: "qemu_img", Name: "qemu-img", Status: Fail,
			Detail: "qemu-img is not installed, so VM disks cannot be created.",
			Fix:    "Install qemu-utils (qemu-img on the RHEL family)."})
	case f.QemuImgFormats != nil && !contains(f.QemuImgFormats, "qcow2"):
		add(Check{ID: "qemu_img", Name: "qemu-img", Status: Fail,
			Detail: "qemu-img " + f.QemuImg + " does not support qcow2, the format of every VM disk.",
			Fix:    "Install the distribution's qemu-utils package rather than a minimal build."})
	default:
		add(pass("qemu_img", "qemu-img", "qemu-img "+f.QemuImg+"."))
	}

	if len(f.Bridges) == 0 {
		add(Check{ID: "bridge", Name: "Bridge", Status: Warn,
			Detail: "No IP pool on this node names a bridge, so new VMs join the default NAT network and cannot be reached from outside.",
			Fix:    "Create a bridge on the uplink (on a new machine the installer's --bridge option does it) and set it on the node's IP pools."})
	}
	for _, b := range f.Bridges {
		add(evalBridge(b))
	}

	for _, p := range f.Pools {
		add(evalPool(p))
	}
	for _, v := range f.VLANBridges {
		add(evalVLANBridge(v))
	}
	for _, b := range sortedKeys(f.DHCP) {
		c := Check{ID: "dhcp:" + b, Name: "DHCP on " + b}
		if problem := f.DHCP[b]; problem != "" {
			c.Status, c.Detail = Warn, "Guests on "+b+" that take their address by DHCP get none: "+problem+"."
			c.Fix = "Install dnsmasq (dnsmasq-base on Debian and Ubuntu) and make sure no other DHCP server holds port 67 on " + b + "; the agent starts it again within a minute."
		} else {
			c.Status, c.Detail = Pass, "Static leases for the addresses assigned on "+b+" are served."
		}
		add(c)
	}

	if f.BridgeNF == netrules.FirewallEnforced {
		add(pass("bridge_netfilter", "Bridge netfilter", "Bridged VM traffic passes through the host firewall, so VM firewall rules apply."))
	} else {
		add(Check{ID: "bridge_netfilter", Name: "Bridge netfilter", Status: Warn,
			Detail: "Bridge netfilter is off: VM firewall rules and the outbound scan guard are installed but filter nothing.",
			Fix:    "On a node without VMs: modprobe br_netfilter and set net.bridge.bridge-nf-call-iptables and -ip6tables to 1 (the installer does this). On a node with running VMs, set MABURVM_BRIDGE_NETFILTER=enforce in the agent's environment at a time you choose and restart the agent."})
	}

	if f.IPForward {
		add(pass("ip_forward", "IP forwarding", "IPv4 forwarding is on."))
	} else {
		add(Check{ID: "ip_forward", Name: "IP forwarding", Status: Warn,
			Detail: "IPv4 forwarding is off: floating IPs, port forwards and VPC routing do not work.",
			Fix:    "Set net.ipv4.ip_forward = 1 in /etc/sysctl.d/99-maburvm.conf and run sysctl --system."})
	}

	out = append(out, evalTools(f.Tools)...)
	out = append(out, evalOptionalHardware(f)...)

	switch f.TimeSynced {
	case "yes":
		add(pass("time_sync", "Clock", "The clock is synchronised."))
	case "no":
		add(Check{ID: "time_sync", Name: "Clock", Status: Warn,
			Detail: "The clock is not synchronised. Two-factor codes, TLS and backup schedules depend on it.",
			Fix:    "Install chrony, or turn on systemd-timesyncd with timedatectl set-ntp true."})
	default:
		add(Check{ID: "time_sync", Name: "Clock", Status: Warn,
			Detail: "Could not tell whether the clock is synchronised (timedatectl did not answer).",
			Fix:    "Make sure chrony or systemd-timesyncd runs: timedatectl status."})
	}
	return out
}

// evalOptionalHardware reports what VMs on the node can be given beyond the
// basics. Each is optional, so a missing one passes and says what it rules out.
func evalOptionalHardware(f Facts) []Check {
	fw := pass("uefi_firmware", "UEFI firmware", "OVMF is installed: UEFI images boot here, with Secure Boot.")
	switch {
	case !f.UEFI:
		fw.Detail = "Optional, not installed: UEFI images (and so Windows 11 and Secure Boot) cannot be installed on this node. Install the ovmf package (edk2-ovmf on the RHEL family) to offer them."
	case !f.SecureBoot:
		fw.Detail = "OVMF is installed without Secure Boot firmware and keys (OVMF_CODE*.secboot.fd, OVMF_VARS*.ms.fd): UEFI images boot here, Secure Boot does not."
	}
	tpm := pass("tpm_emulator", "TPM emulator", "swtpm is installed: VMs can have a virtual TPM 2.0.")
	if !f.TPM {
		tpm.Detail = "Optional, not installed: VMs here cannot have a virtual TPM (Windows 11 needs one). Install swtpm and swtpm-tools to offer it."
	}
	huge := pass("hugepages", "Huge pages", fmt.Sprintf("%d MiB of huge pages are reserved for VMs that ask for them.", f.HugepagesMiB))
	if f.HugepagesMiB == 0 {
		huge.Detail = "Optional, none reserved: VMs with huge pages cannot run on this node. Reserve them with vm.nr_hugepages in /etc/sysctl.d, sized for those VMs."
	}
	return []Check{fw, tpm, huge}
}

func evalBridge(b BridgeFacts) Check {
	c := Check{ID: "bridge:" + b.Name, Name: "Bridge " + b.Name}
	switch {
	case !b.Exists:
		c.Status, c.Detail = Fail, b.Name+" does not exist on this node. VMs attached to it cannot start."
		c.Fix = "Create the bridge on the uplink, or correct the bridge on the node's IP pools."
	case !b.IsBridge:
		c.Status, c.Detail = Fail, b.Name+" exists but is not a bridge."
		c.Fix = "Use the name of a bridge on the node's IP pools, or turn " + b.Name + " into a bridge."
	case !b.Up:
		c.Status, c.Detail = Warn, fmt.Sprintf("%s is down (%d ports), so VMs on it have no network.", b.Name, b.Ports)
		c.Fix = "Add the uplink to the bridge and bring it up: ip link set " + b.Name + " up."
	default:
		c.Status, c.Detail = Pass, fmt.Sprintf("%s is up with %d ports.", b.Name, b.Ports)
	}
	return c
}

func evalVLANBridge(v netrules.VLANBridgeState) Check {
	c := Check{ID: "vlan:" + v.Bridge, Name: "VLAN bridge " + v.Bridge}
	switch {
	case v.Problem != "":
		c.Status, c.Detail = Fail, "VLANs are enabled on "+v.Bridge+" with uplink "+v.Uplink+", but "+v.Problem+". A VM given a VLAN there is not put in it."
		c.Fix = "Correct the bridge and uplink on the node's page (VLAN trunking), or make " + v.Uplink + " a port of " + v.Bridge + "."
	case v.Filtering:
		c.Status, c.Detail = Pass, fmt.Sprintf("%s carries VLANs over %s; VLAN filtering is on.", v.Bridge, v.Uplink)
	default:
		c.Status, c.Detail = Pass, fmt.Sprintf("%s can carry VLANs over %s. VLAN filtering is switched on when the first VM there gets a VLAN; tagged frames that pass the bridge today then need their VLAN added. The switch port of %s must carry the VLANs tagged.", v.Bridge, v.Uplink, v.Uplink)
	}
	return c
}

func evalPool(p PoolFacts) Check {
	c := Check{ID: "pool:" + p.Path, Name: "Storage pool " + p.Path}
	if p.Default {
		c.Name += " (default)"
	}
	if !p.Exists || !p.IsDir {
		c.Status = Fail
		c.Detail = p.Path + " does not exist on this node or is not a directory. New disks for it are refused rather than put on the root filesystem."
		c.Fix = "Create or mount the directory, or correct the pool's path on the Storage page."
		return c
	}
	if !p.Writable {
		c.Status = Fail
		c.Detail = p.Path + " is not writable."
		c.Fix = "Check that its filesystem is mounted read-write; the agent runs as root."
		return c
	}
	c.Status = Pass
	c.Detail = fmt.Sprintf("%s: %s free of %s", p.Path, gb(p.Available), gb(p.Total))
	if p.MountPoint != "" {
		c.Detail += " on " + p.MountPoint
	}
	c.Detail += "."
	var fixes []string
	if p.Total > 0 && p.Available < poolHeadroom {
		c.Status = Fail
		c.Detail += " New VMs need their disk size plus 20 GB free here, so none fit."
		fixes = append(fixes, "Free space in "+p.Path+" or grow its filesystem.")
	}
	if p.MountPoint == "/" {
		if c.Status == Pass {
			c.Status = Warn
		}
		c.Detail += " It is on the root filesystem: when it fills, libvirt, the agent and the system logs run out of space with it."
		fixes = append(fixes, "Mount a separate volume at "+p.Path+", or point the pool at one.")
	}
	c.Fix = strings.Join(fixes, " ")
	return c
}

// toolChecks lists what the agent runs, by check: any one binary satisfies it.
var toolChecks = []struct {
	id, name string
	bins     []string
	status   string
	detail   string
	fix      string
}{
	{"tool_iptables", "iptables", []string{"iptables"}, Fail,
		"iptables is missing: VM firewalls, NAT and port forwards cannot be installed.", "Install iptables."},
	{"tool_iso", "ISO tool", []string{"genisoimage", "mkisofs", "xorriso", "cloud-localds"}, Fail,
		"No ISO tool (genisoimage, mkisofs, xorriso or cloud-localds): new VMs cannot get their first-boot settings.", "Install genisoimage."},
	{"tool_arping", "arping", []string{"arping"}, Warn,
		"arping is missing: before handing out an address the panel cannot see that a machine outside it already uses the address.", "Install iputils-arping (iputils on the RHEL family)."},
	{"tool_ebtables", "ebtables", []string{"ebtables"}, Warn,
		"ebtables is missing: anti-spoofing relies on libvirt's filters alone.", "Install ebtables."},
	{"tool_libguestfs", "libguestfs", []string{"guestfish"}, Warn,
		"libguestfs tools are missing: RouterOS templates and disk image changes fail.", "Install libguestfs-tools."},
	{"tool_dnsmasq", "dnsmasq", []string{"dnsmasq"}, Pass,
		"Optional, not installed: IP pools with DHCP turned on cannot be served on this node.", "Install dnsmasq-base (dnsmasq on the RHEL family)."},
}

// ToolNames is every binary the tool checks look for.
func ToolNames() []string {
	var names []string
	for _, t := range toolChecks {
		names = append(names, t.bins...)
	}
	return names
}

func evalTools(have map[string]bool) []Check {
	out := make([]Check, 0, len(toolChecks))
	for _, t := range toolChecks {
		found := ""
		for _, b := range t.bins {
			if have[b] {
				found = b
				break
			}
		}
		if found != "" {
			out = append(out, pass(t.id, t.name, found+" is installed."))
			continue
		}
		out = append(out, Check{ID: t.id, Name: t.name, Status: t.status, Detail: t.detail, Fix: t.fix})
	}
	return out
}

// Worst is the worst state in the report.
func Worst(checks []Check) string {
	worst := Pass
	for _, c := range checks {
		if c.Status == Fail {
			return Fail
		}
		if c.Status == Warn {
			worst = Warn
		}
	}
	return worst
}

func pass(id, name, detail string) Check {
	return Check{ID: id, Name: name, Status: Pass, Detail: detail}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func gb(b int64) string {
	return fmt.Sprintf("%d GB", b/gib)
}
