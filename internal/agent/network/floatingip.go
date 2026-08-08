package network

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"

	"github.com/coreos/go-iptables/iptables"
)

const (
	// FloatingIPChain holds the inbound DNAT rules for floating IPs. Its jump is
	// APPENDED to PREROUTING, i.e. after MaburVMChain's jump (which is inserted at
	// position 1), so a specific port forward always wins over a floating IP's
	// catch-all "-d <fip> -j DNAT". Reversing that order would silently shadow
	// every existing port forward the moment a floating IP is attached.
	FloatingIPChain = "MABURVM-FIP"
	// FloatingIPPostChain holds the egress SNAT rules for full-mode floating IPs.
	// Its jump is INSERTED at the head of POSTROUTING so it is evaluated before
	// the baseline per-VM MASQUERADE that SetupNAT appends — otherwise the VM
	// would egress as the node uplink and never as its floating IP.
	FloatingIPPostChain = "MABURVM-FIP-POST"
	// FloatingIPFwdChain (filter table) permits the forwarded traffic a floating
	// IP has just been DNATed to. Its jump is INSERTED at the head of FORWARD.
	//
	// This is what makes a floating IP work into a private/managed network:
	// libvirt guards its own NAT networks with an "ACCEPT established, REJECT the
	// rest" pair, so a NEW inbound connection is rejected even though the DNAT
	// succeeded — the address answers ARP, the counters climb, and nothing ever
	// connects. Inserting ahead of that is the only way in. It also makes
	// floating IPs survive a node whose FORWARD policy is DROP.
	FloatingIPFwdChain = "MABURVM-FIP-FWD"
)

// FloatingIPModeFull and FloatingIPModeInbound mirror models.NATMode*; the agent
// keeps its own copies so package network stays free of panel model imports.
const (
	FloatingIPModeInbound = "inbound"
	FloatingIPModeFull    = "full"
)

// ensureFloatingChains creates the floating IP chains and their jumps. Safe to
// call repeatedly.
func (nm *NATManager) ensureFloatingChains() error {
	chains, err := nm.ipt.ListChains(NATTable)
	if err != nil {
		return fmt.Errorf("failed to list chains: %w", err)
	}
	existing := make(map[string]bool, len(chains))
	for _, c := range chains {
		existing[c] = true
	}

	if !existing[FloatingIPChain] {
		if err := nm.ipt.NewChain(NATTable, FloatingIPChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", FloatingIPChain, err)
		}
	}
	if !existing[FloatingIPPostChain] {
		if err := nm.ipt.NewChain(NATTable, FloatingIPPostChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", FloatingIPPostChain, err)
		}
	}

	// PREROUTING: append (evaluated after port forwards).
	ok, err := nm.ipt.Exists(NATTable, PreroutingChain, "-j", FloatingIPChain)
	if err != nil {
		return fmt.Errorf("failed to check PREROUTING jump: %w", err)
	}
	if !ok {
		if err := nm.ipt.Append(NATTable, PreroutingChain, "-j", FloatingIPChain); err != nil {
			return fmt.Errorf("failed to add PREROUTING jump: %w", err)
		}
	}

	// POSTROUTING: must be FIRST, before the baseline MASQUERADE and before
	// libvirt's own chain (see ensureJumpFirst).
	if err := ensureJumpFirst(nm.ipt, NATTable, PostroutingChain, FloatingIPPostChain); err != nil {
		return err
	}

	// FORWARD (filter): insert first, ahead of libvirt's REJECT for its managed
	// networks and ahead of any restrictive per-VM rules.
	fwChains, err := nm.ipt.ListChains(FilterTable)
	if err != nil {
		return fmt.Errorf("failed to list filter chains: %w", err)
	}
	haveFwd := false
	for _, c := range fwChains {
		if c == FloatingIPFwdChain {
			haveFwd = true
			break
		}
	}
	if !haveFwd {
		if err := nm.ipt.NewChain(FilterTable, FloatingIPFwdChain); err != nil {
			return fmt.Errorf("failed to create chain %s: %w", FloatingIPFwdChain, err)
		}
	}
	if err := ensureJumpFirst(nm.ipt, FilterTable, ForwardChain, FloatingIPFwdChain); err != nil {
		return err
	}
	return nil
}

// ensureJumpFirst guarantees that parent's FIRST rule is the jump to target,
// moving an existing jump if something has since been inserted above it.
//
// Checking mere existence is not enough. libvirt re-inserts LIBVIRT_PRT and
// LIBVIRT_FWI/FWO at the TOP of POSTROUTING and FORWARD every time one of its
// networks starts, which silently overtakes a jump added earlier. The
// consequences are both real and were both observed on a live node: in
// POSTROUTING libvirt masquerades the guest to the node's address before our
// SNAT is consulted, so a full-mode floating IP quietly stops being the VM's
// egress identity; in FORWARD libvirt's REJECT lands first and inbound traffic
// to the floating IP is dropped after a successful DNAT.
//
// Since the panel re-applies attachments periodically, this also self-heals a
// node where libvirt has jumped ahead after the fact.
func ensureJumpFirst(ipt *iptables.IPTables, table, parent, target string) error {
	rules, err := ipt.List(table, parent)
	if err != nil {
		return fmt.Errorf("failed to list %s/%s: %w", table, parent, err)
	}
	// rules[0] is the chain policy line; rules[1] is the first actual rule.
	if len(rules) > 1 && strings.HasSuffix(strings.TrimSpace(rules[1]), "-j "+target) {
		return nil // already first
	}
	// Drop any existing (mis-positioned) jump, then put it back at the head.
	for {
		exists, err := ipt.Exists(table, parent, "-j", target)
		if err != nil {
			return fmt.Errorf("failed to check %s/%s jump: %w", table, parent, err)
		}
		if !exists {
			break
		}
		if err := ipt.Delete(table, parent, "-j", target); err != nil {
			return fmt.Errorf("failed to reposition %s/%s jump: %w", table, parent, err)
		}
	}
	if err := ipt.Insert(table, parent, 1, "-j", target); err != nil {
		return fmt.Errorf("failed to add %s/%s jump: %w", table, parent, err)
	}
	return nil
}

// floatingDNATRule builds the inbound 1:1 NAT rule for a floating IP.
func floatingDNATRule(floatingIP, internalIP string) []string {
	return []string{
		"-d", floatingIP + "/32",
		"-j", "DNAT",
		"--to-destination", internalIP,
		"-m", "comment", "--comment", floatingComment(floatingIP),
	}
}

// floatingSNATRule builds the egress rule that makes the VM leave *as* the
// floating IP (full mode only).
func floatingSNATRule(floatingIP, internalIP string) []string {
	return []string{
		"-s", internalIP + "/32",
		"-j", "SNAT",
		"--to-source", floatingIP,
		"-m", "comment", "--comment", floatingComment(floatingIP),
	}
}

// floatingHairpinRule makes a VM that is NOT routed through the host answer on
// the floating IP.
//
// A directly-bridged VM holding its own public address has the upstream gateway
// as its next hop, not this host. Its reply to a DNAT'd connection therefore
// leaves over the bridge and never re-enters the host, so conntrack never gets
// to undo the DNAT: the client sent a SYN to the floating IP and receives a
// SYN-ACK from the VM's own address, which its stack answers with RST. Verified
// on a live node — without this rule the connection never completes.
//
// SNATing to the floating IP forces the reply back through the host, which then
// un-NATs it correctly. The cost is that the guest sees the floating IP as the
// source instead of the real client address.
//
// It is matched on --ctorigdst (the connection's ORIGINAL destination), not just
// -d, so it applies strictly to traffic that arrived on this floating IP. A
// broader match would also catch port-forward connections to the same VM and
// silently rewrite their source too.
func floatingHairpinRule(floatingIP, internalIP string) []string {
	return []string{
		"-d", internalIP + "/32",
		"-m", "conntrack", "--ctorigdst", floatingIP,
		"-j", "SNAT",
		"--to-source", floatingIP,
		"-m", "comment", "--comment", floatingComment(floatingIP),
	}
}

// needsHairpin reports whether a VM's replies bypass the host and so require
// floatingHairpinRule. A private address means the host is the VM's gateway
// (NAT/VPC guest), so its return traffic already transits the host and conntrack
// handles the reversal on its own — and the real client IP is preserved. Any
// other address is directly bridged to the upstream gateway.
func needsHairpin(internalIP string) bool {
	ip := net.ParseIP(internalIP)
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// floatingForwardRule permits packets this floating IP has been DNATed to.
// Scoped by --ctorigdst so it only ever opens the connections that arrived on
// this address — it does not make the VM generally reachable, and it disappears
// with the floating IP on detach.
func floatingForwardRule(floatingIP, internalIP string) []string {
	return []string{
		"-d", internalIP + "/32",
		"-m", "conntrack", "--ctorigdst", floatingIP,
		"-j", "ACCEPT",
		"-m", "comment", "--comment", floatingComment(floatingIP),
	}
}

// floatingComment is keyed by the address alone, so attach/detach match even if
// the floating IP has since been pointed at a different VM.
func floatingComment(floatingIP string) string {
	return fmt.Sprintf("maburvm-fip-%s", floatingIP)
}

// AttachFloatingIP binds floatingIP to the uplink bridge on the host and 1:1-NATs
// it to the VM's own address. Idempotent: re-running it is how the panel restores
// floating IPs after a node reboot, since iptables rules are runtime-only state.
//
// It never touches the baseline per-VM MASQUERADE from SetupNAT, so a VM's
// outbound connectivity is unaffected by attaching (or later detaching) a
// floating IP.
func (nm *NATManager) AttachFloatingIP(floatingIP, internalIP, bridge, natMode string) error {
	if err := validateFloatingArgs(floatingIP, internalIP); err != nil {
		return err
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := nm.ensureFloatingChains(); err != nil {
		return err
	}

	// The host must answer ARP for the address, or upstream traffic never
	// arrives for the DNAT to act on.
	if bridge != "" {
		if err := addHostAddress(floatingIP, bridge); err != nil {
			return err
		}
	}

	// Every rule for this address is replaced, never added to. Repointing a
	// floating IP at another VM changes the rulespec, so appending would leave
	// the OLD DNAT in place ahead of the new one — iptables matches first-wins,
	// so traffic would keep going to the previous VM and the move would silently
	// not happen. Flushing first is what makes attach both idempotent and a
	// correct "move".
	if err := nm.deleteAllMatching(FloatingIPChain, floatingComment(floatingIP)); err != nil {
		return fmt.Errorf("floating IP: clear previous DNAT: %w", err)
	}
	if err := nm.deleteAllSNATFor(floatingIP); err != nil {
		return fmt.Errorf("floating IP: clear previous SNAT: %w", err)
	}
	if err := nm.deleteAllMatchingIn(FilterTable, FloatingIPFwdChain, floatingComment(floatingIP)); err != nil {
		return fmt.Errorf("floating IP: clear previous FORWARD accept: %w", err)
	}

	if err := nm.appendIfMissing(FloatingIPChain, floatingDNATRule(floatingIP, internalIP)); err != nil {
		return fmt.Errorf("floating IP DNAT: %w", err)
	}
	if err := nm.appendIfMissingIn(FilterTable, FloatingIPFwdChain, floatingForwardRule(floatingIP, internalIP)); err != nil {
		return fmt.Errorf("floating IP FORWARD accept: %w", err)
	}

	// Without this a directly-bridged VM's reply bypasses the host entirely and
	// the connection never completes (see floatingHairpinRule). Applied
	// regardless of mode, because it is what makes the address answer at all.
	if needsHairpin(internalIP) {
		if err := nm.appendIfMissing(FloatingIPPostChain, floatingHairpinRule(floatingIP, internalIP)); err != nil {
			return fmt.Errorf("floating IP hairpin SNAT: %w", err)
		}
	}

	if natMode == FloatingIPModeFull {
		if err := nm.appendIfMissing(FloatingIPPostChain, floatingSNATRule(floatingIP, internalIP)); err != nil {
			return fmt.Errorf("floating IP SNAT: %w", err)
		}
	}
	return nil
}

// DetachFloatingIP removes every rule and host address belonging to floatingIP.
// It deliberately touches ONLY the floating IP's own chains — the VM keeps the
// baseline masquerade SetupNAT installed and therefore keeps outbound internet.
func (nm *NATManager) DetachFloatingIP(floatingIP, bridge string) error {
	if net.ParseIP(floatingIP) == nil {
		return fmt.Errorf("invalid floating IP %q", floatingIP)
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := nm.ensureFloatingChains(); err != nil {
		return err
	}
	if err := nm.deleteAllMatching(FloatingIPChain, floatingComment(floatingIP)); err != nil {
		return err
	}
	if err := nm.deleteAllSNATFor(floatingIP); err != nil {
		return err
	}
	if err := nm.deleteAllMatchingIn(FilterTable, FloatingIPFwdChain, floatingComment(floatingIP)); err != nil {
		return err
	}
	if bridge != "" {
		if err := delHostAddress(floatingIP, bridge); err != nil {
			return err
		}
	}
	return nil
}

// deleteAllSNATFor removes the egress rules for a floating IP. Assumes the lock
// is held.
func (nm *NATManager) deleteAllSNATFor(floatingIP string) error {
	return nm.deleteAllMatching(FloatingIPPostChain, floatingComment(floatingIP))
}

// appendIfMissing appends a nat-table rule only when an identical one isn't
// already there.
func (nm *NATManager) appendIfMissing(chain string, rule []string) error {
	return nm.appendIfMissingIn(NATTable, chain, rule)
}

func (nm *NATManager) appendIfMissingIn(table, chain string, rule []string) error {
	exists, err := nm.ipt.Exists(table, chain, rule...)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return nm.ipt.Append(table, chain, rule...)
}

// deleteAllMatching removes every rule in chain whose comment equals the given
// one. Rules are matched by listing the chain rather than by reconstructing the
// rulespec, so detach works even when the caller no longer knows which VM the
// address pointed at or in which mode it was attached.
func (nm *NATManager) deleteAllMatching(chain, comment string) error {
	return nm.deleteAllMatchingIn(NATTable, chain, comment)
}

func (nm *NATManager) deleteAllMatchingIn(table, chain, comment string) error {
	rules, err := nm.ipt.List(table, chain)
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", chain, err)
	}
	// Delete by position, highest first, so earlier positions stay valid.
	// rules[0] is the "-N <chain>" policy line, so rule N is at index N.
	for i := len(rules) - 1; i >= 1; i-- {
		if !floatingRuleMatches(rules[i], comment) {
			continue
		}
		if err := nm.ipt.Delete(table, chain, fmt.Sprintf("%d", i)); err != nil {
			return fmt.Errorf("failed to delete rule %d from %s: %w", i, chain, err)
		}
	}
	return nil
}

// floatingRuleMatches reports whether an `iptables -S` line belongs to the given
// floating IP. iptables always quotes --comment values, so matching the quoted
// form is exact — matching the bare comment would also hit a longer address that
// merely starts with it (…-fip-10.0.0.4 vs …-fip-10.0.0.42), and detaching one
// floating IP would silently tear down another.
func floatingRuleMatches(line, comment string) bool {
	return strings.Contains(line, `"`+comment+`"`)
}

func validateFloatingArgs(floatingIP, internalIP string) error {
	if net.ParseIP(floatingIP) == nil {
		return fmt.Errorf("invalid floating IP %q", floatingIP)
	}
	if net.ParseIP(internalIP) == nil {
		return fmt.Errorf("invalid internal IP %q", internalIP)
	}
	if floatingIP == internalIP {
		return fmt.Errorf("floating IP and internal IP must differ")
	}
	return nil
}

// addHostAddress binds <ip>/32 to the bridge so the host answers ARP for it.
// Already-present is success.
func addHostAddress(ip, bridge string) error {
	out, err := exec.Command("ip", "addr", "add", ip+"/32", "dev", bridge).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "File exists") {
		return fmt.Errorf("ip addr add %s dev %s: %v (%s)", ip, bridge, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// delHostAddress removes <ip>/32 from the bridge. Not-present is success.
func delHostAddress(ip, bridge string) error {
	out, err := exec.Command("ip", "addr", "del", ip+"/32", "dev", bridge).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "Cannot assign requested address") {
		return fmt.Errorf("ip addr del %s dev %s: %v (%s)", ip, bridge, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureForwardingSysctls checks the two kernel switches host-side rules depend
// on. They are usually enabled as a side effect of Docker or libvirt, which is
// exactly what makes an unset one so confusing: the rules are present and simply
// never match.
//
// The two are treated differently on purpose.
//
// ip_forward is SET: without it the host cannot route a DNAT'd packet on to the
// guest, so floating IPs cannot work at all. Enabling routing on a hypervisor
// that is already routing guest traffic changes nothing that was working before.
//
// bridge-nf-call-iptables is only REPORTED, never set. It decides whether
// already-bridged guest traffic is subjected to iptables — so flipping it from 0
// to 1 on a node with live VMs would abruptly start enforcing FORWARD rules that
// have never applied to them, which can cut customer connectivity. That is a
// maintenance-window decision for an operator, not something an agent restart
// should do silently. Floating IPs do not need it (their path is host-routed);
// per-VM firewall rules on bridged VMs do.
func ensureForwardingSysctls() {
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		log.Printf("[NetworkManager] WARNING: could not enable net.ipv4.ip_forward (%v: %s) — "+
			"floating IPs and NAT to guests will not work", err, strings.TrimSpace(string(out)))
	}

	const bridgeNF = "net.bridge.bridge-nf-call-iptables"
	out, err := exec.Command("sysctl", "-n", bridgeNF).CombinedOutput()
	switch {
	case err != nil:
		log.Printf("[NetworkManager] NOTE: %s is unavailable (br_netfilter not loaded). "+
			"Floating IPs are unaffected, but per-VM firewall rules on bridged VMs do not take "+
			"effect. Enabling it changes traffic handling for running VMs — do it in a "+
			"maintenance window, not automatically.", bridgeNF)
	case strings.TrimSpace(string(out)) != "1":
		log.Printf("[NetworkManager] NOTE: %s is 0. Per-VM firewall rules on bridged VMs do not "+
			"take effect. Left unchanged on purpose: flipping it would start enforcing FORWARD "+
			"rules on already-running VMs.", bridgeNF)
	}
}
