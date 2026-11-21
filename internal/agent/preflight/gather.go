package preflight

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/agent/hwprofile"
	"github.com/maburvm/panel/internal/agent/netrules"
	"github.com/maburvm/panel/internal/agent/storage"
)

// Options says what to check beyond the host itself.
type Options struct {
	// Pools are the directory storage pools the panel places disks in. Empty
	// checks DefaultPool, where disks go then.
	Pools       []string
	DefaultPool string
	Bridges     []string
	LibvirtURI  string
	// VLANBridges are the bridges named for per-VM VLANs, with their uplinks.
	VLANBridges map[string]string
	// DHCP is, per bridge the node answers DHCP on, why it is not serving
	// ("" when it is).
	DHCP map[string]string
}

// Run gathers and evaluates in one go.
func Run(ctx context.Context, o Options) []Check { return Evaluate(Gather(ctx, o)) }

// Gather observes the host. It changes nothing but a probe file it creates and
// removes in each pool directory.
func Gather(ctx context.Context, o Options) Facts {
	var f Facts

	if fi, err := os.Stat("/dev/kvm"); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		f.KVMDevice = true
	}
	if runtime.GOARCH == "arm64" {
		// arm64 CPUs advertise no flag for it: KVM there is /dev/kvm or nothing.
		if f.KVMDevice {
			f.CPUVirt = "arm64 EL2"
		}
	} else if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		f.CPUVirt = parseCPUVirt(string(b))
	}

	if _, err := exec.LookPath("virsh"); err == nil {
		f.Virsh = true
		uri := o.LibvirtURI
		if uri == "" {
			uri = "qemu:///system"
		}
		out, err := run(ctx, "virsh", "-c", uri, "version")
		if err != nil {
			f.LibvirtErr = firstLine(out)
			if f.LibvirtErr == "" {
				f.LibvirtErr = err.Error()
			}
		} else {
			f.LibvirtVersion, f.QEMUVersion = parseVirshVersion(out)
		}
	}

	if _, err := exec.LookPath("qemu-img"); err == nil {
		if out, err := run(ctx, "qemu-img", "--version"); err == nil {
			f.QemuImg = parseQemuImgVersion(out)
		}
		if f.QemuImg == "" {
			f.QemuImg = "(unknown version)"
		}
		if out, err := run(ctx, "qemu-img", "--help"); err == nil {
			f.QemuImgFormats = parseQemuImgFormats(out)
		}
	}

	for _, name := range o.Bridges {
		f.Bridges = append(f.Bridges, bridgeFacts(name))
	}
	for _, b := range sortedKeys(o.VLANBridges) {
		f.VLANBridges = append(f.VLANBridges, netrules.CheckVLANBridge("/sys/class/net", b, o.VLANBridges[b]))
	}
	f.DHCP = o.DHCP

	pools, def := o.Pools, false
	if len(pools) == 0 && o.DefaultPool != "" {
		pools, def = []string{o.DefaultPool}, true
	}
	for _, p := range pools {
		pf := poolFacts(p)
		pf.Default = def
		f.Pools = append(f.Pools, pf)
	}

	f.BridgeNF = netrules.BridgeNFState(netrules.BridgeNFProcDir)
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		f.IPForward = strings.TrimSpace(string(b)) == "1"
	}

	f.Tools = map[string]bool{}
	for _, name := range ToolNames() {
		_, err := exec.LookPath(name)
		f.Tools[name] = err == nil
	}

	plain, secure := hwprofile.FindFirmware(hwprofile.Exists)
	f.UEFI, f.SecureBoot = plain != nil, secure != nil
	_, e1 := exec.LookPath("swtpm")
	_, e2 := exec.LookPath("swtpm_setup")
	f.TPM = e1 == nil && e2 == nil
	f.HugepagesMiB, _ = hwprofile.HostHugepagesMiB()

	if out, err := run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil {
		if v := strings.TrimSpace(out); v == "yes" || v == "no" {
			f.TimeSynced = v
		}
	}
	return f
}

func bridgeFacts(name string) BridgeFacts {
	b := BridgeFacts{Name: name}
	// Never let a name from the panel walk out of /sys/class/net.
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return b
	}
	dir := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(dir); err != nil {
		return b
	}
	b.Exists = true
	if fi, err := os.Stat(filepath.Join(dir, "bridge")); err == nil && fi.IsDir() {
		b.IsBridge = true
	}
	if s, err := os.ReadFile(filepath.Join(dir, "operstate")); err == nil {
		b.Up = strings.TrimSpace(string(s)) == "up"
	}
	if ports, err := os.ReadDir(filepath.Join(dir, "brif")); err == nil {
		b.Ports = len(ports)
	}
	return b
}

func poolFacts(path string) PoolFacts {
	p := PoolFacts{Path: path}
	if !filepath.IsAbs(path) {
		return p
	}
	fi, err := os.Stat(path)
	if err != nil {
		return p
	}
	p.Exists, p.IsDir = true, fi.IsDir()
	if !p.IsDir {
		return p
	}
	c := storage.Capacity(path)
	if p.Problem = c.Problem; p.Problem != "" {
		return p // a shared pool that is not mounted: nothing is written there
	}
	if tmp, err := os.CreateTemp(path, ".maburvm-preflight-*"); err == nil {
		p.Writable = true
		tmp.Close()
		os.Remove(tmp.Name())
	}
	p.Total, p.Available, p.MountPoint = c.Total, c.Available, c.Filesystem
	return p
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// parseCPUVirt finds the virtualization flag in /proc/cpuinfo.
func parseCPUVirt(cpuinfo string) string {
	sc := bufio.NewScanner(strings.NewReader(cpuinfo))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		for _, flag := range strings.Fields(val) {
			if flag == "vmx" || flag == "svm" {
				return flag
			}
		}
		return ""
	}
	return ""
}

// parseVirshVersion reads the daemon and hypervisor versions from `virsh version`.
func parseVirshVersion(out string) (libvirtd, qemu string) {
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "Running against daemon":
			libvirtd = val
		case "Running hypervisor":
			qemu = strings.TrimSpace(strings.TrimPrefix(val, "QEMU"))
		}
	}
	return libvirtd, qemu
}

// parseQemuImgVersion reads "qemu-img version 8.2.2 (...)".
func parseQemuImgVersion(out string) string {
	fields := strings.Fields(firstLine(out))
	for i, w := range fields {
		if w == "version" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// parseQemuImgFormats reads the "Supported formats:" line of `qemu-img --help`.
func parseQemuImgFormats(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Supported formats:"); ok {
			return strings.Fields(rest)
		}
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
