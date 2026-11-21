package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/agent/netrules"
)

func healthy() Facts {
	tools := map[string]bool{}
	for _, n := range ToolNames() {
		tools[n] = true
	}
	return Facts{
		KVMDevice: true, CPUVirt: "vmx",
		Virsh: true, LibvirtVersion: "9.0.0", QEMUVersion: "7.2.0",
		QemuImg: "7.2.9", QemuImgFormats: []string{"raw", "qcow2"},
		Bridges:  []BridgeFacts{{Name: "br0", Exists: true, IsBridge: true, Up: true, Ports: 1}},
		Pools:    []PoolFacts{{Path: "/data", Exists: true, IsDir: true, Writable: true, Total: 2000 * gib, Available: 1500 * gib, MountPoint: "/data"}},
		BridgeNF: netrules.FirewallEnforced, IPForward: true, Tools: tools, TimeSynced: "yes",
	}
}

func byID(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, checks)
	return Check{}
}

func TestHealthyHostPasses(t *testing.T) {
	checks := Evaluate(healthy())
	for _, c := range checks {
		if c.Status != Pass {
			t.Errorf("%s: %s (%s)", c.ID, c.Status, c.Detail)
		}
		if c.Fix != "" {
			t.Errorf("%s passed but carries a fix: %q", c.ID, c.Fix)
		}
	}
	if Worst(checks) != Pass {
		t.Fatalf("worst = %s", Worst(checks))
	}
}

func TestEveryProblemSaysHowToFixIt(t *testing.T) {
	f := Facts{Pools: []PoolFacts{{Path: "/missing"}}, Bridges: []BridgeFacts{{Name: "br9"}}, BridgeNF: netrules.FirewallOff}
	checks := Evaluate(f)
	if Worst(checks) != Fail {
		t.Fatalf("an empty host must fail, got %s", Worst(checks))
	}
	for _, c := range checks {
		if c.Status != Pass && c.Fix == "" {
			t.Errorf("%s is %s with no fix", c.ID, c.Status)
		}
	}
}

func TestStatuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Facts)
		id     string
		want   string
		says   string
	}{
		{"no virtualization flag", func(f *Facts) { f.CPUVirt = "" }, "cpu_virtualization", Fail, "no vmx or svm"},
		{"no /dev/kvm", func(f *Facts) { f.KVMDevice = false }, "kvm_device", Fail, "not loaded"},
		{"no virsh", func(f *Facts) { f.Virsh = false }, "libvirt", Fail, "not installed"},
		{"daemon down", func(f *Facts) { f.LibvirtVersion, f.LibvirtErr = "", "failed to connect" }, "libvirt", Fail, "failed to connect"},
		{"no qemu-img", func(f *Facts) { f.QemuImg = "" }, "qemu_img", Fail, "cannot be created"},
		{"no qcow2", func(f *Facts) { f.QemuImgFormats = []string{"raw"} }, "qemu_img", Fail, "qcow2"},
		{"formats not listed", func(f *Facts) { f.QemuImgFormats = nil }, "qemu_img", Pass, ""},
		{"no bridge configured", func(f *Facts) { f.Bridges = nil }, "bridge", Warn, "NAT"},
		{"bridge missing", func(f *Facts) { f.Bridges = []BridgeFacts{{Name: "br0"}} }, "bridge:br0", Fail, "does not exist"},
		{"not a bridge", func(f *Facts) { f.Bridges = []BridgeFacts{{Name: "eth0", Exists: true}} }, "bridge:eth0", Fail, "not a bridge"},
		{"bridge down", func(f *Facts) { f.Bridges[0].Up, f.Bridges[0].Ports = false, 0 }, "bridge:br0", Warn, "down"},
		{"pool missing", func(f *Facts) { f.Pools = []PoolFacts{{Path: "/vm"}} }, "pool:/vm", Fail, "refused"},
		{"pool is a file", func(f *Facts) { f.Pools = []PoolFacts{{Path: "/vm", Exists: true}} }, "pool:/vm", Fail, "not a directory"},
		{"pool read-only", func(f *Facts) { f.Pools[0].Writable = false }, "pool:/data", Fail, "not writable"},
		{"pool full", func(f *Facts) { f.Pools[0].Available = 5 * gib }, "pool:/data", Fail, "none fit"},
		{"pool on root", func(f *Facts) { f.Pools[0].MountPoint = "/" }, "pool:/data", Warn, "root filesystem"},
		{"bridge netfilter off", func(f *Facts) { f.BridgeNF = netrules.FirewallOff }, "bridge_netfilter", Warn, "filter nothing"},
		{"forwarding off", func(f *Facts) { f.IPForward = false }, "ip_forward", Warn, "floating IPs"},
		{"no iptables", func(f *Facts) { f.Tools["iptables"] = false }, "tool_iptables", Fail, "iptables is missing"},
		{"no arping", func(f *Facts) { f.Tools["arping"] = false }, "tool_arping", Warn, "arping is missing"},
		{"no ebtables", func(f *Facts) { f.Tools["ebtables"] = false }, "tool_ebtables", Warn, "ebtables"},
		{"no libguestfs", func(f *Facts) { f.Tools["guestfish"] = false }, "tool_libguestfs", Warn, "RouterOS"},
		{"clock unsynced", func(f *Facts) { f.TimeSynced = "no" }, "time_sync", Warn, "not synchronised"},
		{"clock unknown", func(f *Facts) { f.TimeSynced = "" }, "time_sync", Warn, "Could not tell"},
	}
	for _, c := range cases {
		f := healthy()
		c.mutate(&f)
		got := byID(t, Evaluate(f), c.id)
		if got.Status != c.want || !strings.Contains(got.Detail, c.says) {
			t.Errorf("%s: %s = %s %q, want %s containing %q", c.name, c.id, got.Status, got.Detail, c.want, c.says)
		}
	}
}

func TestISOToolAcceptsAnyAuthoringTool(t *testing.T) {
	f := healthy()
	f.Tools["genisoimage"], f.Tools["mkisofs"] = false, false
	if c := byID(t, Evaluate(f), "tool_iso"); c.Status != Pass || !strings.Contains(c.Detail, "xorriso") {
		t.Fatalf("xorriso alone must do: %+v", c)
	}
	// cloud-localds is the agent's last resort for the seed ISO (cloudinit).
	f.Tools["xorriso"] = false
	if c := byID(t, Evaluate(f), "tool_iso"); c.Status != Pass || !strings.Contains(c.Detail, "cloud-localds") {
		t.Fatalf("cloud-localds alone must do: %+v", c)
	}
	f.Tools["cloud-localds"] = false
	if c := byID(t, Evaluate(f), "tool_iso"); c.Status != Fail {
		t.Fatalf("no ISO tool must fail: %+v", c)
	}
}

func TestFullPoolOnRootFilesystemStaysFailedAndSaysBoth(t *testing.T) {
	f := healthy()
	f.Pools[0].Available, f.Pools[0].MountPoint = gib, "/"
	c := byID(t, Evaluate(f), "pool:/data")
	if c.Status != Fail || !strings.Contains(c.Detail, "none fit") || !strings.Contains(c.Detail, "root filesystem") {
		t.Fatalf("got %+v", c)
	}
	if !strings.Contains(c.Fix, "Free space") || !strings.Contains(c.Fix, "Mount a separate volume") {
		t.Fatalf("fix must cover both: %q", c.Fix)
	}
}

func TestDefaultPoolIsNamedSo(t *testing.T) {
	f := healthy()
	f.Pools = []PoolFacts{{Path: "/var/lib/libvirt/images", Default: true, Exists: true, IsDir: true, Writable: true, Total: 100 * gib, Available: 80 * gib, MountPoint: "/"}}
	c := byID(t, Evaluate(f), "pool:/var/lib/libvirt/images")
	if !strings.HasSuffix(c.Name, "(default)") || c.Status != Warn {
		t.Fatalf("got %+v", c)
	}
}

func TestParsers(t *testing.T) {
	cpu := "processor\t: 0\nflags\t\t: fpu vme de pse svm sse2\n\nprocessor\t: 1\nflags\t\t: fpu vme de pse svm sse2\n"
	if got := parseCPUVirt(cpu); got != "svm" {
		t.Errorf("parseCPUVirt = %q", got)
	}
	if got := parseCPUVirt("flags\t\t: fpu sse2 hypervisor\n"); got != "" {
		t.Errorf("parseCPUVirt without flag = %q", got)
	}

	virsh := "Compiled against library: libvirt 9.0.0\nUsing library: libvirt 9.0.0\nUsing API: QEMU 9.0.0\nRunning hypervisor: QEMU 7.2.0\nRunning against daemon: 9.0.0\n"
	if d, q := parseVirshVersion(virsh); d != "9.0.0" || q != "7.2.0" {
		t.Errorf("parseVirshVersion = %q, %q", d, q)
	}

	if got := parseQemuImgVersion("qemu-img version 8.2.2 (Debian 1:8.2.2+ds-0ubuntu1.4)\nCopyright (c) 2003-2023 Fabrice Bellard\n"); got != "8.2.2" {
		t.Errorf("parseQemuImgVersion = %q", got)
	}

	help := "usage: qemu-img [standard options] command [command options]\n...\nSupported formats: blkdebug file qcow qcow2 raw vmdk\n\nSee <https://qemu.org/contribute/report-a-bug> for how to report bugs.\n"
	formats := parseQemuImgFormats(help)
	if !contains(formats, "qcow2") || len(formats) != 6 {
		t.Errorf("parseQemuImgFormats = %v", formats)
	}
	if parseQemuImgFormats("no list here") != nil {
		t.Error("an unlisted format set must be unknown (nil), not empty")
	}
}

func TestGatherPoolsAndBridgeNames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := Gather(context.Background(), Options{
		Pools:   []string{dir, filepath.Join(dir, "missing"), file, "vg0"},
		Bridges: []string{"../../etc", "no-such-bridge-x"},
	})
	if len(f.Pools) != 4 {
		t.Fatalf("pools = %+v", f.Pools)
	}
	if p := f.Pools[0]; !p.Exists || !p.IsDir || !p.Writable || p.Total <= 0 || p.MountPoint == "" {
		t.Errorf("temp dir: %+v", p)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("the writability probe must not be left behind: %v", left)
	}
	if p := f.Pools[1]; p.Exists {
		t.Errorf("missing dir: %+v", p)
	}
	if p := f.Pools[2]; !p.Exists || p.IsDir {
		t.Errorf("file: %+v", p)
	}
	if p := f.Pools[3]; p.Exists {
		t.Errorf("a relative path is not a directory pool: %+v", p)
	}
	for _, b := range f.Bridges {
		if b.Exists {
			t.Errorf("bridge %q must not resolve", b.Name)
		}
	}

	def := Gather(context.Background(), Options{DefaultPool: dir})
	if len(def.Pools) != 1 || !def.Pools[0].Default {
		t.Fatalf("no pools must check the default: %+v", def.Pools)
	}
}

func TestOptionalHardwareSaysWhatIsMissing(t *testing.T) {
	f := healthy()
	if c := byID(t, Evaluate(f), "uefi_firmware"); c.Status != Pass || !strings.Contains(c.Detail, "ovmf") {
		t.Fatalf("missing OVMF: %+v", c)
	}
	f.UEFI, f.SecureBoot, f.TPM, f.HugepagesMiB = true, true, true, 2048
	checks := Evaluate(f)
	for _, id := range []string{"uefi_firmware", "tpm_emulator", "hugepages"} {
		if c := byID(t, checks, id); c.Status != Pass || strings.Contains(c.Detail, "Optional") {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

func TestCephChecksAreOptional(t *testing.T) {
	f := healthy() // a node with no Ceph client at all
	checks := Evaluate(f)
	if w := Worst(checks); w != Pass {
		t.Fatalf("a node without Ceph must still pass, got %s", w)
	}
	for id, word := range map[string]string{"ceph_cli": "ceph-common", "qemu_rbd": "qemu-block-extra", "libvirt_secrets": "secret"} {
		if c := byID(t, checks, id); c.Status != Pass || !strings.HasPrefix(c.Detail, "Optional") || !strings.Contains(c.Detail, word) {
			t.Errorf("%s: %+v", id, c)
		}
	}
	f.Tools["ceph"], f.Tools["rbd"] = true, true
	f.QemuImgFormats = append(f.QemuImgFormats, "rbd")
	f.LibvirtSecrets = true
	checks = Evaluate(f)
	for _, id := range []string{"ceph_cli", "qemu_rbd", "libvirt_secrets"} {
		if c := byID(t, checks, id); c.Status != Pass || strings.Contains(c.Detail, "Optional") {
			t.Errorf("%s: %+v", id, c)
		}
	}
	f.Tools["rbd"] = false
	if c := byID(t, Evaluate(f), "ceph_cli"); !strings.HasPrefix(c.Detail, "Optional") {
		t.Errorf("ceph without rbd is not a usable client: %+v", c)
	}
}

// A QEMU built with modules lists rbd whether or not the module is there.
func TestRBDNeedsItsModuleWhenQEMUHasModules(t *testing.T) {
	listed := []string{"raw", "qcow2", "rbd"}
	glob := func(files ...string) func(string) ([]string, error) {
		return func(pat string) ([]string, error) {
			var out []string
			for _, f := range files {
				if ok, _ := filepath.Match(pat, f); ok {
					out = append(out, f)
				}
			}
			return out, nil
		}
	}
	for name, tc := range map[string]struct {
		files []string
		rbd   bool
	}{
		"built in, no modules":      {nil, true},
		"Ubuntu with block-extra":   {[]string{"/usr/lib/x86_64-linux-gnu/qemu/block-curl.so", "/usr/lib/x86_64-linux-gnu/qemu/block-rbd.so"}, true},
		"Ubuntu without it":         {[]string{"/usr/lib/x86_64-linux-gnu/qemu/block-curl.so"}, false},
		"RHEL without the rbd part": {[]string{"/usr/lib64/qemu-kvm/block-curl.so"}, false},
		"RHEL with it":              {[]string{"/usr/lib64/qemu-kvm/block-rbd.so"}, true},
	} {
		got := dropMissingModules(append([]string{}, listed...), glob(tc.files...))
		if contains(got, "rbd") != tc.rbd || !contains(got, "qcow2") {
			t.Errorf("%s: formats = %v", name, got)
		}
	}
}

func TestVLANBridgesAndDHCP(t *testing.T) {
	f := healthy()
	f.VLANBridges = []netrules.VLANBridgeState{{Bridge: "br1", Uplink: "bond0"}, {Bridge: "br2", Uplink: "eth3", Problem: "eth3 is not a port of br2"}}
	f.DHCP = map[string]string{"br0": "", "br1": "dnsmasq is not installed on this node"}
	checks := Evaluate(f)
	if c := byID(t, checks, "vlan:br1"); c.Status != Pass {
		t.Errorf("br1: %+v", c)
	}
	if c := byID(t, checks, "vlan:br2"); c.Status != Fail || c.Fix == "" || !strings.Contains(c.Detail, "eth3 is not a port of br2") {
		t.Errorf("br2: %+v", c)
	}
	if c := byID(t, checks, "dhcp:br0"); c.Status != Pass {
		t.Errorf("dhcp br0: %+v", c)
	}
	if c := byID(t, checks, "dhcp:br1"); c.Status != Warn || c.Fix == "" {
		t.Errorf("dhcp br1: %+v", c)
	}
}
