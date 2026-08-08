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

	"github.com/coreos/go-iptables/iptables"
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
)

// ensureScanGuard installs the outbound connection rate limit and any standing
// quarantines. Safe to call repeatedly; the chain is rebuilt from scratch each
// time so it always reflects the current quarantine file.
//
// Why this exists: a single compromised guest running a SYN scan creates a
// conntrack entry per probed destination and can exhaust the node's whole
// conntrack table within minutes. When that happens the node stops accepting
// new connections *for every tenant on it*, and the agent's own RPCs start
// timing out — one guest takes down the node. Observed in production on
// 2026-08-08, which is why the limit is keyed on source address and applied
// node-wide rather than per registered VM: the offending guests were legacy
// ones the agent had never configured.
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

	// Quarantines first: a quarantined guest is dropped outright, never merely
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

	if err := ipt.Append(FilterTable, ScanGuardChain, synFloodRule()...); err != nil {
		return fmt.Errorf("failed to add connection rate limit: %w", err)
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

// synFloodRule drops new outbound TCP connections from a guest that is opening
// them faster than a real workload would.
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

// GuestCounter is one guest's cumulative count of attempted new outbound TCP
// connections, and whether it is currently cut off.
type GuestCounter struct {
	MAC         string
	SYNPackets  int64
	Quarantined bool
}

// ensureCountChain creates the counting chain if absent. It is never flushed:
// the counters are the whole point, and rebuilding it would zero them and hide
// an attack that is already under way.
func ensureCountChain(ipt *iptables.IPTables) error {
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

// countRule tallies new outbound TCP connections from one guest and then hands
// control straight back, so it changes no verdict.
func countRule(mac string) []string {
	return []string{
		"-p", "tcp", "--syn",
		"-m", "mac", "--mac-source", mac,
		"-j", "RETURN",
		"-m", "comment", "--comment", countComment(mac),
	}
}

func countComment(mac string) string { return "maburvm-count-" + strings.ToLower(mac) }

// syncGuestCounters makes the counting chain hold exactly one rule per given
// MAC. Rules for MACs that are still present are left untouched so their
// counters keep accumulating across calls — the panel reads these as cumulative
// values and differences them, so resetting one silently loses a sample.
func syncGuestCounters(ipt *iptables.IPTables, macs []string) error {
	if err := ensureCountChain(ipt); err != nil {
		return err
	}

	want := make(map[string]bool, len(macs))
	for _, m := range macs {
		want[strings.ToLower(m)] = true
	}

	have, err := countedMACs(ipt)
	if err != nil {
		return err
	}

	for mac := range want {
		if !have[mac] {
			if err := ipt.Append(FilterTable, ScanCountChain, countRule(mac)...); err != nil {
				return fmt.Errorf("failed to add counter for %s: %w", mac, err)
			}
		}
	}
	// Drop counters for guests that no longer exist, or the chain grows without
	// bound on a node where VMs come and go.
	for mac := range have {
		if !want[mac] {
			if err := ipt.Delete(FilterTable, ScanCountChain, countRule(mac)...); err != nil {
				log.Printf("[ScanGuard] could not remove stale counter for %s: %v", mac, err)
			}
		}
	}
	return nil
}

// countedMACs returns the MACs that already have a counting rule.
func countedMACs(ipt *iptables.IPTables) (map[string]bool, error) {
	rules, err := ipt.List(FilterTable, ScanCountChain)
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", ScanCountChain, err)
	}
	out := map[string]bool{}
	for _, r := range rules {
		if mac := macFromComment(r, "maburvm-count-"); mac != "" {
			out[mac] = true
		}
	}
	return out, nil
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

// GuestConnectionCounters reports, for each given guest MAC, how many new
// outbound TCP connections it has attempted and whether it is quarantined.
//
// macs comes from the caller because only the agent's libvirt side can enumerate
// domains — including the ones the panel has never seen, which is exactly where
// the abuse came from.
func (m *Manager) GuestConnectionCounters(macs []string) ([]GuestCounter, error) {
	ipt := m.firewall.ipt

	if err := syncGuestCounters(ipt, macs); err != nil {
		return nil, err
	}

	rules, err := ipt.ListWithCounters(FilterTable, ScanCountChain)
	if err != nil {
		return nil, fmt.Errorf("failed to read counters: %w", err)
	}
	counts := make(map[string]int64, len(rules))
	for _, r := range rules {
		mac := macFromComment(r, "maburvm-count-")
		if mac == "" {
			continue
		}
		if hit := counterRE.FindStringSubmatch(r); len(hit) == 2 {
			n, err := strconv.ParseInt(hit[1], 10, 64)
			if err != nil {
				continue
			}
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

	out := make([]GuestCounter, 0, len(macs))
	for _, mac := range macs {
		mac = strings.ToLower(mac)
		out = append(out, GuestCounter{
			MAC:         mac,
			SYNPackets:  counts[mac],
			Quarantined: isQuarantined[mac],
		})
	}
	return out, nil
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
