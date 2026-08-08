package network

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
)

const (
	// ScanGuardChain rate-limits how fast any guest may open new outbound TCP
	// connections, and holds the quarantine rules for guests taken off the
	// network by hand.
	ScanGuardChain = "MABURVM-SCANGUARD"

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
