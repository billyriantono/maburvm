package handler

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The installer is exercised in --dry-run against a scratch root
// (MABURVM_INSTALL_ROOT) with every system command it calls stubbed, so its
// safety rules run here without a Linux host.

const testInstallToken = "mbe_0123456789abcdef0123456789abcdef0123456789abcdef"

// bash4 finds a bash new enough for the installer, or skips.
func bash4(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash", "/usr/bin/bash"} {
		out, err := exec.Command(p, "-c", "echo ${BASH_VERSINFO[0]}").Output()
		if err == nil && strings.TrimSpace(string(out)) >= "4" && len(strings.TrimSpace(string(out))) == 1 {
			return p
		}
	}
	t.Skip("no bash 4+ to run the installer with")
	return ""
}

type installHost struct {
	t     *testing.T
	root  string
	stubs string
	env   []string
	// noAgents serves the script from a panel with no agent bundled.
	noAgents bool
}

func newInstallHost(t *testing.T) *installHost {
	h := &installHost{t: t, root: t.TempDir(), stubs: t.TempDir()}
	h.file("etc/os-release", "ID=debian\nVERSION_ID=\"12\"\n")
	h.file("proc/cpuinfo", "flags\t\t: fpu vmx sse2\n")
	h.file("dev/kvm", "")
	// Every command the installer may reach, so a real one on the test host is never run.
	h.stub("uname", `echo x86_64`)
	h.stub("curl", `out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
[ -n "$out" ] && printf '%s' "${STUB_CURL_BODY:-{\"success\":true,\"data\":{\"node_name\":\"node-new\"}}}" > "$out"
printf '%s' "${STUB_CURL_CODE:-200}"`)
	h.stub("virsh", `printf '%s' "${STUB_VIRSH:-}"`)
	h.stub("nmcli", `printf '%s' "${STUB_NMCLI:-}"`)
	h.stub("resolvectl", `echo "Link 2 (eth0): 192.0.2.53 192.0.2.54"`)
	h.stub("netplan", `[ "$1" = get ] || exit 1; f="$(dirname "$0")/netplan-$2"; if [ -f "$f" ]; then cat "$f"; else echo null; fi`)
	h.stub("ip", `case "$*" in
"-4 route show default") echo "default via 192.0.2.1 dev eth0 proto static" ;;
"-o addr show dev eth0 scope global") printf '%s\n' "2: eth0    inet 192.0.2.10/24 brd 192.0.2.255 scope global eth0\\       valid_lft forever preferred_lft forever" "2: eth0    inet6 2001:db8::10/64 scope global \\       valid_lft forever preferred_lft forever" ;;
"-4 route show default dev eth0") echo "default via 192.0.2.1 proto static onlink" ;;
"-6 route show default dev eth0") echo "default via fe80::1 proto static metric 1024 pref medium" ;;
"-4 route show dev eth0") printf '%s\n' "default via 192.0.2.1 proto static onlink" "192.0.2.0/24 proto kernel scope link src 192.0.2.10" ${STUB_ROUTES4:+"$STUB_ROUTES4"} ;;
"-6 route show dev eth0") printf '%s\n' "2001:db8::/64 proto kernel metric 256 pref medium" "fe80::/64 proto kernel metric 256 pref medium" ;;
*) exit 1 ;;
esac`)
	for _, name := range []string{"apt-cache", "apt-get", "dnf", "systemctl", "systemd-run", "modprobe", "sysctl", "ufw", "timedatectl", "id"} {
		h.stub(name, `exit 0`)
	}
	return h
}

func (h *installHost) file(rel, content string) {
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installHost) stub(name, body string) {
	if err := os.WriteFile(filepath.Join(h.stubs, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installHost) netplanGet(key, yaml string) {
	if err := os.WriteFile(filepath.Join(h.stubs, "netplan-"+key), []byte(yaml), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installHost) uplink() {
	h.file("sys/class/net/eth0/address", "52:54:00:12:34:56\n")
	h.file("sys/class/net/eth0/mtu", "1500\n")
}

func (h *installHost) tree() []string {
	var files []string
	_ = filepath.Walk(h.root, func(p string, _ os.FileInfo, _ error) error {
		files = append(files, p)
		return nil
	})
	sort.Strings(files)
	return files
}

// install serves the script from the panel and runs it with args.
func (h *installHost) install(args ...string) (string, int) {
	h.t.Helper()
	agents := map[string][]byte{"agent-amd64": []byte("\x7fELF-amd64"), "agent-arm64": []byte("\x7fELF-arm64")}
	if h.noAgents {
		agents = nil
	}
	f := newProvisionFixture(h.t, agents)
	rec := f.do(h.t, http.MethodGet, "/install-agent.sh", "")
	script := filepath.Join(h.t.TempDir(), "install-agent.sh")
	if err := os.WriteFile(script, rec.Body.Bytes(), 0o755); err != nil {
		h.t.Fatal(err)
	}
	before := h.tree()
	cmd := exec.Command(bash4(h.t), append([]string{script}, args...)...)
	cmd.Env = append([]string{"PATH=" + h.stubs + ":/usr/bin:/bin", "MABURVM_INSTALL_ROOT=" + h.root, "HOME=" + h.root}, h.env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			h.t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	if after := h.tree(); strings.Join(after, "\n") != strings.Join(before, "\n") {
		h.t.Fatalf("a dry run changed the host:\nbefore %v\nafter %v", before, after)
	}
	return out.String(), code
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestInstaller_DryRunOnANewMachine(t *testing.T) {
	h := newInstallHost(t)
	out, code := h.install("--dry-run", "--token", testInstallToken, "--pool-dir", "/data/vms")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out,
		`Installing node "node-new"`,
		"apt-get install -y --no-install-recommends libvirt-daemon-system libvirt-clients qemu-utils iputils-arping ebtables iptables bridge-utils libguestfs-tools genisoimage chrony",
		"qemu-system-x86", "libvirt-daemon-driver-qemu",
		"install -d -m 0711 "+h.root+"/data/vms",
		"write /etc/modules-load.d/maburvm.conf", "| br_netfilter",
		"| net.bridge.bridge-nf-call-iptables = 1", "| net.bridge.bridge-nf-call-ip6tables = 1", "| net.ipv4.ip_forward = 1",
		"check its SHA-256", "enroll with the panel",
		"write /etc/maburvm-agent.env (mode 0600)", "| MABURVM_BRIDGE_NETFILTER=enforce",
		"| EnvironmentFile=/etc/maburvm-agent.env", "| ExecStart=/usr/local/bin/maburvm-agent", "| KillMode=mixed",
		"systemctl enable --now maburvm-agent",
		"--preflight --pool /data/vms",
		"NEEDRESTART_MODE=a apt-get install",
		"| MABURVM_DATA_DIR=/var/lib/maburvm",
		"nothing was changed")
	if strings.Contains(out, "bridges:") || strings.Contains(out, "systemd-run") {
		t.Errorf("no --bridge must leave the network alone:\n%s", out)
	}
	// The agent is put in place only once enrolling worked: an agent binary
	// left by a failed enrollment would make the next run refuse the host.
	if enroll, place := strings.Index(out, "enroll with the panel"), strings.Index(out, "install the checked agent as /usr/local/bin/maburvm-agent"); place == -1 || place < enroll {
		t.Errorf("the agent must be installed after enrolling (enroll at %d, install at %d):\n%s", enroll, place, out)
	}
}

func TestInstaller_NoBundledAgentStopsBeforeInstallingAnything(t *testing.T) {
	h := newInstallHost(t)
	h.noAgents = true
	out, code := h.install("--dry-run", "--token", testInstallToken)
	if code != 1 || !strings.Contains(out, "no SHA-256 published for amd64") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "apt-get") {
		t.Fatalf("nothing may be installed when there is no agent to install:\n%s", out)
	}
}

func TestInstaller_RefusesAHostThatRunsVMs(t *testing.T) {
	h := newInstallHost(t)
	h.env = append(h.env, "STUB_VIRSH=customer-vm-1\ncustomer-vm-2\n")
	out, code := h.install("--dry-run", "--token", testInstallToken)
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	mustContain(t, out, "libvirt runs domains: customer-vm-1 customer-vm-2", "not a new machine")
	if strings.Contains(out, "apt-get") {
		t.Fatal("nothing may be installed after the refusal")
	}

	// --force goes on, but leaves the network and bridge netfilter alone.
	out, code = h.install("--dry-run", "--token", testInstallToken, "--force")
	if code != 0 {
		t.Fatalf("--force: exit %d:\n%s", code, out)
	}
	mustContain(t, out, "--force: carrying on", "bridge netfilter left as it is")
	if strings.Contains(out, "MABURVM_BRIDGE_NETFILTER=enforce") || strings.Contains(out, "99-maburvm-bridge.conf") {
		t.Errorf("a host with VMs must not get bridge netfilter turned on:\n%s", out)
	}

	// Even forced, it will not move a busy host's network onto a new bridge.
	h.uplink()
	out, code = h.install("--dry-run", "--token", testInstallToken, "--force", "--bridge", "br0")
	if code != 3 || !strings.Contains(out, "not creating br0") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestInstaller_RefusesAnExistingAgentOrDomainFiles(t *testing.T) {
	for _, rel := range []string{"etc/systemd/system/maburvm-agent.service", "usr/local/bin/maburvm-agent", "etc/maburvm-agent.env", "etc/libvirt/qemu/old-vm.xml"} {
		h := newInstallHost(t)
		h.file(rel, "x")
		out, code := h.install("--dry-run", "--token", testInstallToken)
		if code != 3 || !strings.Contains(out, "not a new machine") {
			t.Errorf("%s: exit %d:\n%s", rel, code, out)
		}
	}
}

func TestInstaller_RefusesBadInput(t *testing.T) {
	cases := []struct {
		name string
		prep func(*installHost)
		args []string
		says string
	}{
		{"unsupported system", func(h *installHost) { h.file("etc/os-release", "ID=debian\nVERSION_ID=\"11\"\n") }, nil, "unsupported system (debian 11)"},
		{"not a token", nil, []string{"--token", "abc"}, "not an install token"},
		{"no token", nil, []string{"--pool-dir", "/x"}, "--token is required"},
		{"relative pool", nil, []string{"--dry-run", "--pool-dir", "data"}, "absolute path"},
		{"panel refuses token", func(h *installHost) {
			h.env = append(h.env, "STUB_CURL_CODE=403", `STUB_CURL_BODY={"success":false,"message":"the install token is wrong, expired or already used."}`)
		}, []string{"--dry-run", "--token", testInstallToken}, "did not accept the install token (HTTP 403): the install token is wrong"},
		{"unknown option", nil, []string{"--frobnicate"}, "unknown option"},
	}
	for _, c := range cases {
		h := newInstallHost(t)
		if c.prep != nil {
			c.prep(h)
		}
		args := c.args
		if args == nil {
			args = []string{"--dry-run", "--token", testInstallToken}
		}
		out, code := h.install(args...)
		if code != 1 || !strings.Contains(out, c.says) {
			t.Errorf("%s: exit %d, want 1 saying %q:\n%s", c.name, code, c.says, out)
		}
	}
}

func TestInstaller_BridgeOnNetplan(t *testing.T) {
	h := newInstallHost(t)
	h.uplink()
	h.file("etc/netplan/50-cloud-init.yaml", "network: {}\n")
	h.file("etc/cloud/cloud.cfg.d/.keep", "")
	h.netplanGet("ethernets", "eth0:\n  addresses:\n  - \"192.0.2.10/24\"\n")
	out, code := h.install("--dry-run", "--token", testInstallToken, "--bridge", "br0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out,
		"Moving eth0 onto a new bridge br0 (netplan)",
		"|   bridges:", "|     br0:", "|       interfaces: [eth0]", `|       macaddress: "52:54:00:12:34:56"`,
		`|         - "192.0.2.10/24"`, `|         - "2001:db8::10/64"`,
		"|           via: 192.0.2.1", "|           on-link: true", `|           via: "fe80::1"`,
		"|         addresses: [192.0.2.53, 192.0.2.54]",
		"| install -m 0600 /var/backups/maburvm-network-", "| netplan apply",
		"99-maburvm-disable-network-config.cfg",
		"| ip addr replace 192.0.2.10/24 dev eth0 || true",
		"| ip route replace default via 192.0.2.1 dev eth0 onlink || true",
		"systemd-run --quiet --unit=maburvm-net-rollback-", "--on-active=150s",
		"systemd-run --quiet --wait --collect --unit=maburvm-net-apply-",
		`ask for "keep" within 120 seconds`,
		"--preflight --pool /var/lib/libvirt/images --bridge br0")
	// The rollback is armed before the change is applied.
	if strings.Index(out, "maburvm-net-rollback-") > strings.Index(out, "maburvm-net-apply-") {
		t.Error("the rollback timer must be armed before the new network is applied")
	}
}

func TestInstaller_BridgeOnIfupdown(t *testing.T) {
	h := newInstallHost(t)
	h.uplink()
	h.file("etc/network/interfaces", "auto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet static\n    address 192.0.2.10/24\n    gateway 192.0.2.1\n")
	out, code := h.install("--dry-run", "--token", testInstallToken, "--bridge", "br0")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out,
		"(ifupdown)",
		"| iface eth0 inet manual", "| iface br0 inet static", "|     address 192.0.2.10/24", "|     bridge_ports eth0",
		"|     hwaddress ether 52:54:00:12:34:56", "|     up ip route replace default via 192.0.2.1 dev br0 onlink",
		"| iface br0 inet6 static", "|     address 2001:db8::10/64",
		"| ip link set dev eth0 master br0", "| ip addr replace 192.0.2.10/24 dev br0",
		"| cp -a /var/backups/maburvm-network-")
}

func TestInstaller_BridgeRefusals(t *testing.T) {
	cases := []struct {
		name string
		prep func(*installHost)
		says string
	}{
		{"NetworkManager", func(h *installHost) {
			h.file("etc/netplan/01-nm.yaml", "network: {renderer: NetworkManager}\n")
			h.env = append(h.env, "STUB_NMCLI=eth0:connected\nlo:unmanaged\n")
		}, "NetworkManager manages eth0"},
		{"netplan with a second interface", func(h *installHost) {
			h.file("etc/netplan/50-cloud-init.yaml", "network: {}\n")
			h.netplanGet("ethernets", "eth0:\n  dhcp4: true\neth1:\n  addresses: [10.0.0.2/24]\n")
		}, "configures more than eth0"},
		{"netplan with a bond", func(h *installHost) {
			h.file("etc/netplan/50-cloud-init.yaml", "network: {}\n")
			h.netplanGet("ethernets", "eth0:\n  dhcp4: true\n")
			h.netplanGet("bonds", "bond0:\n  interfaces: [eth0]\n")
		}, "configures more than eth0"},
		{"ifupdown with a second interface", func(h *installHost) {
			h.file("etc/network/interfaces", "iface eth0 inet static\n    address 192.0.2.10/24\niface eth1 inet dhcp\n")
		}, "configures more than eth0"},
		{"unknown network setup", func(h *installHost) {}, "could not tell how this host's network is configured"},
		{"uplink with an extra static route", func(h *installHost) {
			h.file("etc/netplan/50-cloud-init.yaml", "network: {}\n")
			h.netplanGet("ethernets", "eth0:\n  addresses: [192.0.2.10/24]\n")
			h.env = append(h.env, "STUB_ROUTES4=10.0.0.0/8 via 192.0.2.254 proto static")
		}, "would lose them"},
	}
	for _, c := range cases {
		h := newInstallHost(t)
		h.uplink()
		c.prep(h)
		out, code := h.install("--dry-run", "--token", testInstallToken, "--bridge", "br0")
		if code != 1 || !strings.Contains(out, c.says) {
			t.Errorf("%s: exit %d, want 1 saying %q:\n%s", c.name, code, c.says, out)
		}
		if strings.Contains(out, "systemd-run") {
			t.Errorf("%s: nothing may be applied after a refusal", c.name)
		}
	}
}

func TestInstaller_UsesAnExistingBridgeAsItIs(t *testing.T) {
	h := newInstallHost(t)
	h.file("sys/class/net/br0/bridge/.keep", "")
	out, code := h.install("--dry-run", "--token", testInstallToken, "--bridge", "br0")
	if code != 0 || !strings.Contains(out, "Using the existing bridge br0; the network is not changed") || strings.Contains(out, "systemd-run") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
