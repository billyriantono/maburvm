package network

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-iptables/iptables"

	"github.com/maburvm/panel/internal/agent/netrules"
)

const (
	// ScanGuardChain rate-limits how fast any guest may open new outbound TCP
	// connections, and holds the quarantine rules for guests taken off the
	// network by hand.
	ScanGuardChain = "MABURVM-SCANGUARD"

	// ScanCountChain holds one counting rule per guest. Its rules all end in
	// RETURN, so they only tally and never decide anything; the jump to it sits
	// at the head of ScanGuardChain so a quarantined guest is still counted and
	// the operator can see an attack continuing rather than a flat line.
	ScanCountChain = "MABURVM-SCANCOUNT"

	// QuarantineFile lists MAC addresses whose traffic is dropped, one per line;
	// "#" starts a comment. A file rather than panel state on purpose: the guests
	// that need this most are the ones the panel does not manage, and it must
	// survive a reboot without the panel being reachable.
	QuarantineFile = "/etc/maburvm/quarantine"

	// synRateAbove and synRateBurst bound new outbound connections per source
	// address. A guest running a busy site opens new connections in the low tens
	// per second; the scanner observed on a live node ran at ~3,100/s, so this
	// leaves normal workloads untouched while cutting a scan down to noise.
	//
	// ponytail: fixed thresholds, make them per-plan settings if a customer ever
	// has a legitimate workload above them.
	synRateAbove = "50/sec"
	synRateBurst = "100"

	// SynLimitChain holds the rate limit itself. Only the per-guest counting
	// rules jump to it, so it sees nothing but connections a guest opens. It
	// used to sit straight in ScanGuardChain, where it also met every SYN
	// arriving from the internet: a busy site behind a CDN, whose edge
	// addresses each open hundreds of connections a second, had them dropped.
	SynLimitChain = "MABURVM-SYNLIMIT"

	// NullRouteFile lists addresses whose inbound traffic is dropped until a
	// set time. A file for the same reason as QuarantineFile, and because a
	// null-route must lift itself on time even with the panel unreachable.
	NullRouteFile = "/etc/maburvm/nullroutes"

	// NullRoute6Chain holds the IPv6 null-routes; IPv4 ones live at the head
	// of ScanGuardChain.
	NullRoute6Chain = "MABURVM-NULLROUTE"
)

// counterMu serialises rewrites of ScanCountChain: the agent's own ticker and
// the panel's poll both sync it, and two syncs racing would add a rule twice.
var counterMu sync.Mutex

// ensureScanGuard installs the outbound connection rate limit and any standing
// quarantines. Safe to call repeatedly; the chain is rebuilt from scratch each
// time so it always reflects the current quarantine file.
//
// Why this exists: a single compromised guest running a SYN scan creates a
// conntrack entry per probed destination and can exhaust the node's whole
// conntrack table within minutes. When that happens the node stops accepting
// new connections *for every tenant on it*, and the agent's own RPCs start
// timing out — one guest takes down the node. Observed in production on
// 2026-08-08, which is why the limit covers every guest libvirt runs on the
// node (see syncGuestCounters) rather than only VMs the panel registered: the
// offending guests were legacy ones the agent had never configured.
func ensureScanGuard(ipt *iptables.IPTables) error {
	chains, err := ipt.ListChains(FilterTable)
	if err != nil {
		return fmt.Errorf("failed to list filter chains: %w", err)
	}
	exists := false
	for _, c := range chains {
		if c == ScanGuardChain {
			exists = true
			break
		}
	}
	if !exists {
		if err := ipt.NewChain(FilterTable, ScanGuardChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", ScanGuardChain, err)
		}
	} else if err := ipt.ClearChain(FilterTable, ScanGuardChain); err != nil {
		return fmt.Errorf("failed to flush chain %s: %w", ScanGuardChain, err)
	}

	// Null-routes first: traffic to an address under attack is dropped before
	// it costs anything further down.
	for _, r := range readNullRoutes() {
		if _, v6, _ := netrules.NullRouteIP(r.IP); v6 {
			continue
		}
		if err := ipt.Append(FilterTable, ScanGuardChain, netrules.NullRouteRule(r.IP)...); err != nil {
			return fmt.Errorf("failed to null-route %s: %w", r.IP, err)
		}
	}

	// Quarantines next: a quarantined guest is dropped outright, never merely
	// rate-limited.
	macs, err := readQuarantine(QuarantineFile)
	if err != nil {
		log.Printf("[ScanGuard] WARNING: could not read %s (%v) — no quarantines applied", QuarantineFile, err)
	}
	for _, mac := range macs {
		if err := ipt.Append(FilterTable, ScanGuardChain, quarantineRule(mac)...); err != nil {
			return fmt.Errorf("failed to quarantine %s: %w", mac, err)
		}
		log.Printf("[ScanGuard] quarantined %s: forwarded traffic dropped", mac)
	}

	if err := ensureSynLimit(ipt, true); err != nil {
		return err
	}

	// Counting comes first so a quarantined guest still shows up in the numbers.
	// Inserted rather than appended for that reason, and after the rules above so
	// a half-built chain never sits in the path.
	if err := ensureCountChain(ipt); err != nil {
		return err
	}
	if err := ipt.Insert(FilterTable, ScanGuardChain, 1, "-j", ScanCountChain); err != nil {
		return fmt.Errorf("failed to add counter jump: %w", err)
	}

	// FORWARD only carries guest traffic — the host's own connections go through
	// OUTPUT — so putting this at the head of FORWARD limits guests without ever
	// touching the node itself. It must be first for the same reason the floating
	// IP chains must be: libvirt re-inserts its own chains above anything added
	// earlier, and an ACCEPT there would let a scan past.
	return ensureJumpFirst(ipt, FilterTable, ForwardChain, ScanGuardChain)
}

// ensureSynLimit creates SynLimitChain with its one rule. rebuild flushes it
// first; without it an existing chain is left alone, so a counter sync never
// rewrites a chain packets are passing through.
func ensureSynLimit(ipt *iptables.IPTables, rebuild bool) error {
	ok, err := ipt.ChainExists(FilterTable, SynLimitChain)
	if err != nil {
		return fmt.Errorf("failed to check chain %s: %w", SynLimitChain, err)
	}
	switch {
	case !ok:
		if err := ipt.NewChain(FilterTable, SynLimitChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", SynLimitChain, err)
		}
	case rebuild:
		if err := ipt.ClearChain(FilterTable, SynLimitChain); err != nil {
			return fmt.Errorf("failed to flush chain %s: %w", SynLimitChain, err)
		}
	default:
		return nil
	}
	if err := ipt.Append(FilterTable, SynLimitChain, synFloodRule()...); err != nil {
		return fmt.Errorf("failed to add connection rate limit: %w", err)
	}
	return nil
}

// synFloodRule drops new outbound TCP connections from a guest that is opening
// them faster than a real workload would. It lives in SynLimitChain, which
// only guest-originated SYNs reach.
//
// Matching on --syn (rather than conntrack NEW) keeps retransmitted SYNs of an
// already-counted connection out of the tally, so a guest talking to an
// unreachable host is not punished for retrying. htable-expire reclaims idle
// per-source counters so the hash table cannot grow without bound.
func synFloodRule() []string {
	return []string{
		"-p", "tcp", "--syn",
		"-m", "hashlimit",
		"--hashlimit-above", synRateAbove,
		"--hashlimit-burst", synRateBurst,
		"--hashlimit-mode", "srcip",
		"--hashlimit-name", "maburvm-syn",
		"--hashlimit-htable-expire", "60000",
		"-j", "DROP",
		"-m", "comment", "--comment", "maburvm-scanguard-synrate",
	}
}

// quarantineRule drops everything a guest sends, leaving the guest itself
// running so its owner keeps console access and their data.
//
// Keyed on MAC, not IP, deliberately: a guest that misbehaves may well be
// running a spoofed or duplicated address — two live guests were found sharing
// one address on 2026-08-08 — and the MAC is the only identifier the host
// assigns and the guest cannot quietly change.
func quarantineRule(mac string) []string {
	return []string{
		"-m", "mac", "--mac-source", mac,
		"-j", "DROP",
		"-m", "comment", "--comment", "maburvm-quarantine-" + mac,
	}
}

// GuestCounter is one guest's cumulative counts of new outbound and inbound
// TCP connections and of packets each way, and whether it is cut off.
type GuestCounter struct {
	MAC          string
	SYNPackets   int64
	SYNInPackets int64
	// PacketsOut and PacketsIn are what the guest sent and received, read from
	// its tap, so they keep counting whatever netfilter does to the traffic.
	PacketsOut  int64
	PacketsIn   int64
	Quarantined bool
}

// GuestNIC is a guest interface and its host-side tap ("" when unknown).
type GuestNIC struct {
	MAC string
	Tap string
}

// ensureCountChain creates the counting chain if absent. It is never flushed
// in the normal course: the counters are the whole point, and rebuilding it
// would zero them and hide an attack that is already under way.
func ensureCountChain(ipt *iptables.IPTables) error {
	if err := ensureSynLimit(ipt, false); err != nil {
		return err
	}
	ok, err := ipt.ChainExists(FilterTable, ScanCountChain)
	if err != nil {
		return fmt.Errorf("failed to check chain %s: %w", ScanCountChain, err)
	}
	if !ok {
		if err := ipt.NewChain(FilterTable, ScanCountChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", ScanCountChain, err)
		}
	}
	return nil
}

// countRule tallies new outbound TCP connections from one guest and sends them
// through the rate limit. Keyed on the MAC so only the guest's own SYNs are
// limited; legacy guests with taps named by another panel are covered too.
func countRule(mac string) []string {
	return []string{
		"-p", "tcp", "--syn",
		"-m", "mac", "--mac-source", mac,
		"-j", SynLimitChain,
		"-m", "comment", "--comment", countComment(mac),
	}
}

func countComment(mac string) string { return "maburvm-count-" + strings.ToLower(mac) }

// countInRule tallies new TCP connections arriving for one guest. It only
// counts: RETURN leaves the verdict to the rules after it. physdev is what
// names the tap a bridged frame leaves by, and adding it loads br_netfilter,
// so syncGuestCounters only adds it where bridge netfilter is already on.
func countInRule(mac, tap string) []string {
	return []string{
		"-p", "tcp", "--syn",
		"-m", "physdev", "--physdev-out", tap, "--physdev-is-bridged",
		"-j", "RETURN",
		"-m", "comment", "--comment", countInComment(mac),
	}
}

func countInComment(mac string) string { return "maburvm-countin-" + strings.ToLower(mac) }

// physdevOutRE reads the tap back out of a listed inbound counting rule.
var physdevOutRE = regexp.MustCompile(`--physdev-out (\S+)`)

// syncGuestCounters makes the counting chain hold exactly one outbound rule
// per guest, and one inbound rule per guest whose tap is known. Rules that are
// still right are left untouched so their counters keep accumulating — the
// panel reads these as cumulative values and differences them, so resetting
// one silently loses a sample.
func syncGuestCounters(ipt *iptables.IPTables, nics []GuestNIC) error {
	counterMu.Lock()
	defer counterMu.Unlock()
	if err := ensureCountChain(ipt); err != nil {
		return err
	}

	// The kernel loads br_netfilter by itself the first time a physdev match
	// is added, which would start filtering every bridged guest on a node
	// where the operator has not turned that on (see ensureBridgeNetfilter).
	// So inbound counters exist only where it is already on; elsewhere they
	// read zero and any left from before are removed.
	inbound := FirewallEnforcement() == netrules.FirewallEnforced
	want := make(map[string]string, len(nics))
	for _, n := range nics {
		tap := n.Tap
		if !inbound {
			tap = ""
		}
		want[strings.ToLower(n.MAC)] = tap
	}

	rules, err := ipt.List(FilterTable, ScanCountChain)
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", ScanCountChain, err)
	}
	haveOut := map[string]bool{}
	haveIn := map[string]string{}
	legacy := false
	for _, r := range rules {
		if mac := macFromComment(r, "maburvm-countin-"); mac != "" {
			if m := physdevOutRE.FindStringSubmatch(r); len(m) == 2 {
				haveIn[mac] = m[1]
			}
			continue
		}
		if mac := macFromComment(r, "maburvm-count-"); mac != "" {
			haveOut[mac] = true
			// Agents before the rate limit moved out of ScanGuardChain counted
			// with RETURN; those rules would leave guests unlimited.
			legacy = legacy || !strings.Contains(r, "-j "+SynLimitChain)
		}
	}
	if legacy {
		if err := ipt.ClearChain(FilterTable, ScanCountChain); err != nil {
			return fmt.Errorf("failed to replace old counting rules: %w", err)
		}
		haveOut, haveIn = map[string]bool{}, map[string]string{}
	}

	for mac, tap := range want {
		if !haveOut[mac] {
			if err := ipt.Append(FilterTable, ScanCountChain, countRule(mac)...); err != nil {
				return fmt.Errorf("failed to add counter for %s: %w", mac, err)
			}
		}
		old, had := haveIn[mac]
		if had && old != tap {
			// The guest restarted onto a new tap: the old rule counts nothing.
			if err := ipt.Delete(FilterTable, ScanCountChain, countInRule(mac, old)...); err != nil {
				log.Printf("[ScanGuard] could not remove stale inbound counter for %s: %v", mac, err)
			}
			had = false
		}
		if !had && tap != "" {
			if err := ipt.Append(FilterTable, ScanCountChain, countInRule(mac, tap)...); err != nil {
				log.Printf("[ScanGuard] could not add inbound counter for %s on %s: %v", mac, tap, err)
			}
		}
	}
	// Drop counters for guests that no longer exist, or the chain grows without
	// bound on a node where VMs come and go.
	for mac := range haveOut {
		if _, ok := want[mac]; !ok {
			if err := ipt.Delete(FilterTable, ScanCountChain, countRule(mac)...); err != nil {
				log.Printf("[ScanGuard] could not remove stale counter for %s: %v", mac, err)
			}
		}
	}
	for mac, tap := range haveIn {
		if _, ok := want[mac]; !ok {
			if err := ipt.Delete(FilterTable, ScanCountChain, countInRule(mac, tap)...); err != nil {
				log.Printf("[ScanGuard] could not remove stale inbound counter for %s: %v", mac, err)
			}
		}
	}
	return nil
}

// macFromComment pulls the MAC out of a rule's comment. iptables quotes comments
// only when they contain no shell-special characters, so both forms appear.
func macFromComment(rule, prefix string) string {
	i := strings.Index(rule, prefix)
	if i < 0 {
		return ""
	}
	rest := rule[i+len(prefix):]
	end := strings.IndexAny(rest, "\" ")
	if end >= 0 {
		rest = rest[:end]
	}
	if _, err := net.ParseMAC(rest); err != nil {
		return ""
	}
	return strings.ToLower(rest)
}

// counterRE extracts the packet count iptables reports with `-v -S`, which
// renders it as "-c <packets> <bytes>" just before the target.
var counterRE = regexp.MustCompile(`-c (\d+) \d+`)

// GuestConnectionCounters reports, for each given guest, how many new TCP
// connections it has opened and received, its packets each way, and whether it
// is quarantined.
//
// nics comes from the caller because only the agent's libvirt side can
// enumerate domains — including the ones the panel has never seen, which is
// exactly where the abuse came from.
func (m *Manager) GuestConnectionCounters(nics []GuestNIC) ([]GuestCounter, error) {
	ipt := m.firewall.ipt

	if err := syncGuestCounters(ipt, nics); err != nil {
		return nil, err
	}

	rules, err := ipt.ListWithCounters(FilterTable, ScanCountChain)
	if err != nil {
		return nil, fmt.Errorf("failed to read counters: %w", err)
	}
	counts := make(map[string]int64, len(rules))
	countsIn := make(map[string]int64, len(rules))
	for _, r := range rules {
		hit := counterRE.FindStringSubmatch(r)
		if len(hit) != 2 {
			continue
		}
		n, err := strconv.ParseInt(hit[1], 10, 64)
		if err != nil {
			continue
		}
		if mac := macFromComment(r, "maburvm-countin-"); mac != "" {
			countsIn[mac] = n
		} else if mac := macFromComment(r, "maburvm-count-"); mac != "" {
			counts[mac] = n
		}
	}

	quarantined, err := readQuarantine(QuarantineFile)
	if err != nil {
		return nil, err
	}
	isQuarantined := make(map[string]bool, len(quarantined))
	for _, q := range quarantined {
		isQuarantined[q] = true
	}

	out := make([]GuestCounter, 0, len(nics))
	for _, n := range nics {
		mac := strings.ToLower(n.MAC)
		c := GuestCounter{
			MAC:          mac,
			SYNPackets:   counts[mac],
			SYNInPackets: countsIn[mac],
			Quarantined:  isQuarantined[mac],
		}
		if n.Tap != "" {
			// The tap's receive side is what the guest sent.
			c.PacketsOut = readInt64File("/sys/class/net/" + n.Tap + "/statistics/rx_packets")
			c.PacketsIn = readInt64File("/sys/class/net/" + n.Tap + "/statistics/tx_packets")
		}
		out = append(out, c)
	}
	return out, nil
}

// SyncGuestCounters keeps the counting rules (and so the outbound rate limit)
// in step with the guests running now, without waiting for the panel to ask:
// a guest started between two polls, or while the panel is down, is limited
// from the agent's next tick.
func (m *Manager) SyncGuestCounters(nics []GuestNIC) {
	if err := syncGuestCounters(m.firewall.ipt, nics); err != nil {
		log.Printf("[ScanGuard] WARNING: counter sync failed: %v", err)
	}
}

// readNullRoutes returns the null-routes in force. An unreadable file reads
// as none: a null-route only ever cuts traffic, so failing open is right.
func readNullRoutes() []netrules.NullRoute {
	raw, err := os.ReadFile(NullRouteFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ScanGuard] WARNING: could not read %s: %v", NullRouteFile, err)
		}
		return nil
	}
	routes, _ := netrules.ParseNullRoutes(string(raw), time.Now())
	return routes
}

// NullRoutes lists the null-routes in force on this node.
func (m *Manager) NullRoutes() []netrules.NullRoute {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return readNullRoutes()
}

// SetNullRoute drops all traffic to ip for the given time, or lifts it. Like
// SetQuarantine, the file is written and the rules rebuilt in one step, and the
// full list is returned for the caller to reconcile against.
func (m *Manager) SetNullRoute(ip string, d time.Duration, reason string, on bool) ([]netrules.NullRoute, error) {
	ip, v6, err := netrules.NullRouteIP(ip)
	if err != nil {
		return nil, err
	}
	if on && (d <= 0 || d > netrules.MaxNullRoute) {
		return nil, fmt.Errorf("a null-route lasts between a minute and %s", netrules.MaxNullRoute)
	}
	if on && v6 && m.firewall.ipt6 == nil {
		return nil, fmt.Errorf("this node has no ip6tables, so it cannot null-route %s", ip)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	next := netrules.SetNullRoute(readNullRoutes(), ip, time.Now().Add(d), reason, on)
	if err := m.applyNullRoutes(next); err != nil {
		return nil, err
	}
	return next, nil
}

// ExpireNullRoutes lifts null-routes whose time is up. Run on the agent's own
// ticker, so they end on time whether or not the panel is reachable.
func (m *Manager) ExpireNullRoutes() {
	raw, err := os.ReadFile(NullRouteFile)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	routes, expired := netrules.ParseNullRoutes(string(raw), time.Now())
	if expired == 0 {
		return
	}
	if err := m.applyNullRoutes(routes); err != nil {
		log.Printf("[ScanGuard] WARNING: could not lift expired null-routes: %v", err)
		return
	}
	log.Printf("[ScanGuard] lifted %d expired null-route(s)", expired)
}

// applyNullRoutes writes the file and rebuilds the rules that carry it. The
// caller holds m.mu.
func (m *Manager) applyNullRoutes(routes []netrules.NullRoute) error {
	if err := os.MkdirAll(filepath.Dir(NullRouteFile), 0o755); err != nil {
		return err
	}
	tmp := NullRouteFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(netrules.FormatNullRoutes(routes)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, NullRouteFile); err != nil {
		return err
	}
	if err := ensureScanGuard(m.firewall.ipt); err != nil {
		return fmt.Errorf("null-route file written but IPv4 rules not applied: %w", err)
	}
	if ipt6 := m.firewall.ipt6; ipt6 != nil {
		if err := applyNullRoutes6(ipt6, routes); err != nil {
			return fmt.Errorf("null-route file written but IPv6 rules not applied: %w", err)
		}
	}
	return nil
}

// applyNullRoutes6 rebuilds the IPv6 null-route chain at the head of FORWARD.
// Nothing is created on a node that has never had an IPv6 null-route.
func applyNullRoutes6(ipt6 *iptables.IPTables, routes []netrules.NullRoute) error {
	var v6 []string
	for _, r := range routes {
		if _, isV6, _ := netrules.NullRouteIP(r.IP); isV6 {
			v6 = append(v6, r.IP)
		}
	}
	exists, err := ipt6.ChainExists(FilterTable, NullRoute6Chain)
	if err != nil {
		return err
	}
	if !exists && len(v6) == 0 {
		return nil
	}
	if !exists {
		if err := ipt6.NewChain(FilterTable, NullRoute6Chain); err != nil {
			return err
		}
	} else if err := ipt6.ClearChain(FilterTable, NullRoute6Chain); err != nil {
		return err
	}
	for _, ip := range v6 {
		if err := ipt6.Append(FilterTable, NullRoute6Chain, netrules.NullRouteRule(ip)...); err != nil {
			return err
		}
	}
	return ensureJumpFirst(ipt6, FilterTable, ForwardChain, NullRoute6Chain)
}

// SetQuarantine cuts a guest off the network or puts it back, and returns the
// full list afterwards so the caller reconciles against the node rather than
// trusting its own view — the file is also meant to be editable by hand.
//
// The file is rewritten and the chain rebuilt in one step, so the node's running
// state and the file that survives a reboot never disagree.
func (m *Manager) SetQuarantine(mac, reason string, on bool) ([]string, error) {
	parsed, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return nil, fmt.Errorf("%q is not a MAC address", mac)
	}
	mac = strings.ToLower(parsed.String())

	m.mu.Lock()
	defer m.mu.Unlock()

	current, err := readQuarantine(QuarantineFile)
	if err != nil {
		return nil, err
	}
	reasons, err := readQuarantineReasons(QuarantineFile)
	if err != nil {
		return nil, err
	}

	next := current[:0:0]
	for _, existing := range current {
		if existing != mac {
			next = append(next, existing)
		}
	}
	if on {
		next = append(next, mac)
		reasons[mac] = sanitiseReason(reason)
	} else {
		delete(reasons, mac)
	}

	if err := writeQuarantine(QuarantineFile, next, reasons); err != nil {
		return nil, err
	}
	if err := ensureScanGuard(m.firewall.ipt); err != nil {
		return nil, fmt.Errorf("quarantine file written but rules not applied: %w", err)
	}
	return next, nil
}

// sanitiseReason keeps a reason to one safe line: it is written into a config
// file that an operator reads later, and a newline in it would silently forge
// extra entries.
func sanitiseReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '#' {
			return ' '
		}
		return r
	}, reason)
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return reason
}

// writeQuarantine rewrites the file atomically, so a crash mid-write cannot
// leave a truncated file that silently releases every quarantined guest.
func writeQuarantine(path string, macs []string, reasons map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Guests whose forwarded traffic is dropped. One MAC per line.\n")
	b.WriteString("# The guest keeps running (console + data intact); only its network is cut.\n")
	b.WriteString("# Managed by the panel, but safe to edit by hand — restart maburvm-agent after.\n")
	for _, mac := range macs {
		if r := reasons[mac]; r != "" {
			fmt.Fprintf(&b, "%s   # %s\n", mac, r)
		} else {
			fmt.Fprintf(&b, "%s\n", mac)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readQuarantineReasons recovers the note written next to each MAC so rewriting
// the file does not discard why the other entries are there.
func readQuarantineReasons(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		mac, note, _ := strings.Cut(line, "#")
		mac = strings.ToLower(strings.TrimSpace(mac))
		if mac == "" {
			continue
		}
		if _, err := net.ParseMAC(mac); err != nil {
			continue
		}
		out[mac] = strings.TrimSpace(note)
	}
	return out, scanner.Err()
}

// ConntrackUsage reports the kernel's connection tracking table. A zero max
// means the counters are unreadable (no nf_conntrack), which the caller should
// treat as "unknown" rather than "empty".
func ConntrackUsage() (count, max int64) {
	return readInt64File("/proc/sys/net/netfilter/nf_conntrack_count"),
		readInt64File("/proc/sys/net/netfilter/nf_conntrack_max")
}

func readInt64File(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// readQuarantine parses the quarantine file, skipping blanks, comments and
// anything that is not a MAC address. A missing file means "nothing
// quarantined" and is not an error.
func readQuarantine(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var macs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if _, err := net.ParseMAC(line); err != nil {
			log.Printf("[ScanGuard] ignoring %q in %s: not a MAC address", line, path)
			continue
		}
		macs = append(macs, line)
	}
	return macs, scanner.Err()
}
