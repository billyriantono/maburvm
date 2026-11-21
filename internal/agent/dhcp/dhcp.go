// Package dhcp answers DHCP on the node's bridges for the addresses the panel
// assigned, so a guest installed without cloud-init (from an ISO, say) gets
// its address without anyone typing it in.
//
// One dnsmasq runs per bridge with nothing but static leases: each assigned
// address goes to exactly one MAC, the VM's card on that bridge, and a client
// it does not know gets no answer at all. It never hands out an address from a
// range, never answers DNS, and keeps no lease file. Leases are infinite, so a
// guest never has to reach the server again after its first answer; an address
// change reaches such a guest when it next asks (at boot).
package dhcp

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// Host is one static lease: the address the VM's card on Bridge is given.
type Host struct {
	Bridge  string   `json:"bridge"`
	MAC     string   `json:"mac"`
	IP      string   `json:"ip"`
	Prefix  int      `json:"prefix"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

func (h Host) validate() error {
	if !models.ValidInterfaceName(h.Bridge) {
		return fmt.Errorf("invalid bridge %q", h.Bridge)
	}
	if hw, err := net.ParseMAC(h.MAC); err != nil || len(hw) != 6 {
		return fmt.Errorf("invalid MAC %q", h.MAC)
	}
	if ip := net.ParseIP(h.IP); ip == nil || ip.To4() == nil || h.Prefix < 1 || h.Prefix > 32 {
		return fmt.Errorf("invalid address %s/%d", h.IP, h.Prefix)
	}
	if h.Gateway != "" {
		if ip := net.ParseIP(h.Gateway); ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid gateway %q", h.Gateway)
		}
	}
	for _, d := range h.DNS {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
	}
	return nil
}

// subnet is one network served on a bridge, with the options its guests get.
type subnet struct {
	network, mask, gateway string
	dns                    []string
}

// Config is the dnsmasq configuration for one bridge. Hosts are sorted so the
// same leases always render the same file.
func Config(bridge, pidFile string, hosts []Host) string {
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].IP < hosts[j].IP })
	var subnets []subnet
	tag := map[string]int{}
	for _, h := range hosts {
		_, n, _ := net.ParseCIDR(fmt.Sprintf("%s/%d", h.IP, h.Prefix))
		key := n.String() + "|" + h.Gateway + "|" + strings.Join(h.DNS, ",")
		if _, ok := tag[key]; !ok {
			tag[key] = len(subnets)
			subnets = append(subnets, subnet{network: n.IP.String(), mask: net.IP(n.Mask).String(), gateway: h.Gateway, dns: h.DNS})
		}
	}
	var b strings.Builder
	b.WriteString("# Written by the MaburVM agent: static leases only, from the panel's address assignments.\n")
	fmt.Fprintf(&b, "port=0\ninterface=%s\nbind-dynamic\nexcept-interface=lo\nleasefile-ro\npid-file=%s\ndhcp-ignore=tag:!known\n", bridge, pidFile)
	for i, s := range subnets {
		fmt.Fprintf(&b, "dhcp-range=set:s%d,%s,static,%s,infinite\n", i, s.network, s.mask)
		if s.gateway != "" {
			// The bridge need not hold an address in the subnet; this names
			// one that is, so dnsmasq serves it there.
			fmt.Fprintf(&b, "shared-network=%s,%s\n", bridge, s.gateway)
			fmt.Fprintf(&b, "dhcp-option=tag:s%d,option:router,%s\n", i, s.gateway)
		}
		var v4 []string
		for _, d := range s.dns {
			if net.ParseIP(d).To4() != nil {
				v4 = append(v4, d)
			}
		}
		if len(v4) > 0 {
			fmt.Fprintf(&b, "dhcp-option=tag:s%d,option:dns-server,%s\n", i, strings.Join(v4, ","))
		}
	}
	for _, h := range hosts {
		fmt.Fprintf(&b, "dhcp-host=%s,%s,infinite\n", strings.ToLower(h.MAC), h.IP)
	}
	return b.String()
}

// Manager keeps one dnsmasq per bridge that has leases, and the leases of
// every VM on disk so an agent restart serves them again at once.
type Manager struct {
	mu    sync.Mutex
	dir   string
	hosts map[string][]Host // by VM id
	procs map[string]*proc  // by bridge
	// start runs dnsmasq; replaced in tests.
	start func(conf string) (*exec.Cmd, error)
}

type proc struct {
	cmd  *exec.Cmd
	conf string
	done chan struct{}
	err  string
}

// NewManager loads the saved leases from dir and starts serving them. A node
// without dnsmasq still records them; each bridge then reports why.
func NewManager(dir string) *Manager {
	m := &Manager{dir: dir, hosts: map[string][]Host{}, procs: map[string]*proc{}, start: startDnsmasq}
	if b, err := os.ReadFile(m.statePath()); err == nil {
		if err := json.Unmarshal(b, &m.hosts); err != nil {
			log.Printf("[DHCP] WARNING: ignoring unreadable %s: %v", m.statePath(), err)
			m.hosts = map[string][]Host{}
		}
	}
	m.mu.Lock()
	m.syncLocked()
	m.mu.Unlock()
	go func() {
		for range time.Tick(time.Minute) {
			m.mu.Lock()
			m.syncLocked() // a dnsmasq that died is started again
			m.mu.Unlock()
		}
	}()
	return m
}

func (m *Manager) statePath() string { return filepath.Join(m.dir, "dhcp-hosts.json") }

// Set replaces the leases of a VM; nil removes them. Invalid ones are dropped
// with a log line, never served.
func (m *Manager) Set(vmID string, hosts []Host) {
	if m == nil {
		return
	}
	var ok []Host
	for _, h := range hosts {
		if err := h.validate(); err != nil {
			log.Printf("[DHCP] VM %s: lease not served: %v", vmID, err)
			continue
		}
		ok = append(ok, h)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(ok) == 0 {
		if _, had := m.hosts[vmID]; !had {
			return
		}
		delete(m.hosts, vmID)
	} else {
		m.hosts[vmID] = ok
	}
	m.save()
	m.syncLocked()
}

func (m *Manager) save() {
	b, err := json.MarshalIndent(m.hosts, "", "  ")
	if err == nil {
		if err = os.MkdirAll(m.dir, 0o700); err == nil {
			tmp := m.statePath() + ".tmp"
			if err = os.WriteFile(tmp, b, 0o600); err == nil {
				err = os.Rename(tmp, m.statePath())
			}
		}
	}
	if err != nil {
		log.Printf("[DHCP] WARNING: could not save leases: %v", err)
	}
}

// byBridge groups every lease by bridge. A MAC or address claimed twice on a
// bridge keeps its first holder only, so no address is ever handed to two cards.
func (m *Manager) byBridge() map[string][]Host {
	vms := make([]string, 0, len(m.hosts))
	for id := range m.hosts {
		vms = append(vms, id)
	}
	sort.Strings(vms)
	out := map[string][]Host{}
	seen := map[string]bool{}
	for _, id := range vms {
		for _, h := range m.hosts[id] {
			mk, ik := h.Bridge+"|"+strings.ToLower(h.MAC), h.Bridge+"|"+h.IP
			if seen[mk] || seen[ik] {
				log.Printf("[DHCP] VM %s: %s or %s is already leased on %s; not served twice", id, h.MAC, h.IP, h.Bridge)
				continue
			}
			seen[mk], seen[ik] = true, true
			out[h.Bridge] = append(out[h.Bridge], h)
		}
	}
	return out
}

// syncLocked starts, restarts and stops dnsmasq so each bridge with leases
// has one serving exactly them. Callers hold m.mu.
func (m *Manager) syncLocked() {
	want := m.byBridge()
	for bridge, p := range m.procs {
		if _, ok := want[bridge]; !ok {
			p.stop()
			delete(m.procs, bridge)
			_ = os.Remove(m.confPath(bridge))
		}
	}
	for bridge, hosts := range want {
		conf := Config(bridge, m.pidPath(bridge), hosts)
		if p, ok := m.procs[bridge]; ok && p.conf == conf && p.running() {
			continue
		}
		if p, ok := m.procs[bridge]; ok {
			p.stop()
		}
		m.procs[bridge] = m.launch(bridge, conf)
	}
}

func (m *Manager) confPath(bridge string) string { return filepath.Join(m.dir, "dhcp-"+bridge+".conf") }
func (m *Manager) pidPath(bridge string) string  { return filepath.Join(m.dir, "dhcp-"+bridge+".pid") }

func (m *Manager) launch(bridge, conf string) *proc {
	p := &proc{conf: conf, done: make(chan struct{})}
	killStale(m.pidPath(bridge))
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		p.err = err.Error()
		close(p.done)
		return p
	}
	if err := os.WriteFile(m.confPath(bridge), []byte(conf), 0o644); err != nil {
		p.err = err.Error()
		close(p.done)
		return p
	}
	cmd, err := m.start(m.confPath(bridge))
	if err != nil {
		p.err = err.Error()
		close(p.done)
		log.Printf("[DHCP] not serving %s: %v", bridge, err)
		return p
	}
	p.cmd = cmd
	go func() {
		err := cmd.Wait()
		if err != nil {
			p.err = "dnsmasq stopped: " + err.Error()
			log.Printf("[DHCP] dnsmasq on %s stopped: %v", bridge, err)
		}
		close(p.done)
	}()
	log.Printf("[DHCP] serving static leases on %s", bridge)
	return p
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) stop() {
	if p.cmd == nil || !p.running() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// Status says, per bridge with leases, why it is not served; "" when it is.
func (m *Manager) Status() map[string]string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for bridge, p := range m.procs {
		if p.running() {
			out[bridge] = ""
		} else if out[bridge] = p.err; p.err == "" {
			out[bridge] = "dnsmasq is not running"
		}
	}
	return out
}

func startDnsmasq(conf string) (*exec.Cmd, error) {
	bin, err := exec.LookPath("dnsmasq")
	if err != nil {
		return nil, fmt.Errorf("dnsmasq is not installed on this node (install dnsmasq-base, or dnsmasq on the RHEL family)")
	}
	cmd := exec.Command(bin, "--keep-in-foreground", "--conf-file="+conf)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// killStale stops a dnsmasq an earlier agent left running for this bridge,
// which would otherwise hold the DHCP port.
func killStale(pidFile string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err != nil || strings.TrimSpace(string(comm)) != "dnsmasq" {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
}
