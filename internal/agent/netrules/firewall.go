package netrules

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
)

// VMFirewall is everything the host filters for one VM.
type VMFirewall struct {
	VMID string
	// IPs are the VM's IPv4 addresses, primary first; every one gets the same
	// policy, or an address the rules don't mention sits wide open.
	IPs []string
	// IPv6Prefix is the routed prefix or on-link /128, empty when none.
	IPv6Prefix string
	Rules      []models.FirewallRule
	// DefaultAccept leaves inbound traffic no rule matched alone instead of
	// dropping it. Either way a VM without rules is not filtered.
	DefaultAccept bool
	BlockSMTP     bool
}

// FirewallSpecs is one family's rules of a VM. Top goes at the head of the
// chain in this order (first matched first); Bottom is appended after every
// VM's rules and the conntrack accept, so return traffic of connections the VM
// opened is accepted before a default drop sees it.
type FirewallSpecs struct {
	Top, Bottom [][]string
}

// Len is how many chain entries the specs make.
func (s FirewallSpecs) Len() int { return len(s.Top) + len(s.Bottom) }

// VMRuleTag is the comment prefix every firewall entry of a VM carries.
func VMRuleTag(vmID string) string { return "maburvm-vm-" + vmID + "-" }

// NextGeneration picks the generation for a re-apply: whichever the VM's
// installed entries do not carry, so the new set can go in beside the old one
// and the old one can then be removed by its exact spec.
func NextGeneration(lines []string, vmID string) string {
	cur := VMRuleTag(vmID) + "a-"
	for _, l := range lines {
		if strings.Contains(l, cur) {
			return "b"
		}
	}
	return "a"
}

// smtpPort is outbound mail delivery; submission ports (465, 587) stay open.
const smtpPort = "25"

// BuildFirewall turns a VM's firewall into iptables (v4) and ip6tables (v6)
// specs tagged with generation gen. Every rule is checked before anything is
// returned: one bad rule must leave the VM with the rules it had.
func BuildFirewall(fw VMFirewall, gen string) (v4, v6 FirewallSpecs, err error) {
	sorted := make([]models.FirewallRule, len(fw.Rules))
	copy(sorted, fw.Rules)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })
	for _, r := range sorted {
		if err := validateRule(r); err != nil {
			return FirewallSpecs{}, FirewallSpecs{}, fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}

	tag := VMRuleTag(fw.VMID) + gen + "-"
	comment := func(what string) []string { return []string{"-m", "comment", "--comment", tag + what} }
	prefix := fw.IPv6Prefix

	// The operator's mail block comes first: a customer's own allow rule must
	// not reopen it.
	if fw.BlockSMTP {
		block := func(src string) []string {
			return append(append([]string{"-s", src, "-p", "tcp", "--dport", smtpPort}, comment("smtp")...),
				"-j", "REJECT", "--reject-with", "tcp-reset")
		}
		for _, ip := range fw.IPs {
			v4.Top = append(v4.Top, block(ip))
		}
		if prefix != "" {
			v6.Top = append(v6.Top, block(prefix))
		}
	}

	for _, r := range sorted {
		if r.Family != models.FirewallFamilyV6 {
			for _, ip := range fw.IPs {
				v4.Top = append(v4.Top, ruleSpecs(fw.VMID, r, ip, false, comment)...)
			}
		}
		if r.Family != models.FirewallFamilyV4 && prefix != "" {
			v6.Top = append(v6.Top, ruleSpecs(fw.VMID, r, prefix, true, comment)...)
		}
	}

	if len(fw.Rules) > 0 && !fw.DefaultAccept {
		for _, ip := range fw.IPs {
			v4.Bottom = append(v4.Bottom, append(append([]string{"-d", ip}, comment("default-drop")...), "-j", "DROP"))
		}
		if prefix != "" {
			v6.Bottom = append(v6.Bottom, append(append([]string{"-d", prefix}, comment("default-drop")...), "-j", "DROP"))
		}
	}
	return v4, v6, nil
}

// ruleSpecs builds one rule for one of the VM's addresses (local): the LOG
// entry when the rule logs, then the rule itself. Nil when the rule cannot
// apply to this family (its remote end or ICMP type belongs to the other).
func ruleSpecs(vmID string, r models.FirewallRule, local string, v6 bool, comment func(string) []string) [][]string {
	var match []string
	switch r.Protocol {
	case "", "all":
	case "icmp":
		if v6 {
			match = append(match, "-p", "icmpv6")
		} else {
			match = append(match, "-p", "icmp")
		}
	default:
		match = append(match, "-p", r.Protocol)
	}

	remote := strings.TrimSpace(r.SourceIP)
	if remote == "0.0.0.0/0" || remote == "::/0" {
		remote = ""
	}
	if remote != "" && isIPv4(remote) == v6 {
		return nil
	}
	// SourceIP is the remote end: where inbound traffic comes from, or where
	// outbound traffic goes to.
	if r.Direction == "inbound" {
		if remote != "" {
			match = append(match, "-s", remote)
		}
		match = append(match, "-d", local)
	} else {
		match = append(match, "-s", local)
		if remote != "" {
			match = append(match, "-d", remote)
		}
	}

	if r.PortRange != "" && (r.Protocol == "tcp" || r.Protocol == "udp") {
		spec, multi, _ := normalizePortSpec(r.PortRange) // validated by the caller
		if multi {
			match = append(match, "-m", "multiport", "--dports", spec)
		} else {
			match = append(match, "--dport", spec)
		}
	}
	if r.Protocol == "icmp" && r.ICMPType != "" {
		inV4, inV6, _ := models.ICMPTypeFamilies(r.ICMPType)
		if (v6 && !inV6) || (!v6 && !inV4) {
			return nil
		}
		if v6 {
			match = append(match, "--icmpv6-type", r.ICMPType)
		} else {
			match = append(match, "--icmp-type", r.ICMPType)
		}
	}

	var target []string
	switch r.Action {
	case "allow":
		target = []string{"-j", "ACCEPT"}
	case "reject":
		target = []string{"-j", "REJECT"}
		if r.Protocol == "tcp" {
			target = append(target, "--reject-with", "tcp-reset")
		}
	default:
		target = []string{"-j", "DROP"}
	}

	var out [][]string
	if r.Log {
		logSpec := append([]string{}, match...)
		logSpec = append(logSpec, "-m", "limit", "--limit", "10/min", "--limit-burst", "20")
		logSpec = append(logSpec, comment("log-"+r.ID)...)
		out = append(out, append(logSpec, "-j", "LOG", "--log-prefix", LogPrefix(vmID, r.Action)))
	}
	spec := append(append([]string{}, match...), comment("rule-"+r.ID)...)
	return append(out, append(spec, target...))
}

// LogPrefix starts each kernel log line of a VM's logged rule, so the lines can
// be told apart per VM and action (iptables allows 29 characters).
func LogPrefix(vmID, action string) string {
	short := vmID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("MBVM %s %s ", short, strings.ToUpper(action))
}

func isIPv4(cidrOrIP string) bool {
	ip := net.ParseIP(strings.SplitN(cidrOrIP, "/", 2)[0])
	return ip != nil && ip.To4() != nil
}

// validateRule checks one rule as the agent receives it.
func validateRule(r models.FirewallRule) error {
	switch r.Protocol {
	case "tcp", "udp", "icmp", "all":
	default:
		return fmt.Errorf("invalid protocol: %s", r.Protocol)
	}
	switch r.Action {
	case "allow", "deny", "reject":
	default:
		return fmt.Errorf("invalid action: %s", r.Action)
	}
	switch r.Direction {
	case "inbound", "outbound":
	default:
		return fmt.Errorf("invalid direction: %s", r.Direction)
	}
	switch r.Family {
	case "", models.FirewallFamilyV4, models.FirewallFamilyV6:
	default:
		return fmt.Errorf("invalid address family: %s", r.Family)
	}
	if r.PortRange != "" {
		if _, _, err := normalizePortSpec(r.PortRange); err != nil {
			return err
		}
	}
	if r.Priority < 1 || r.Priority > 1000 {
		return fmt.Errorf("invalid priority: %d (must be 1-1000)", r.Priority)
	}
	return r.CheckCombination()
}

// maxMultiportEntries is iptables' own limit on -m multiport --dports.
const maxMultiportEntries = 15

// normalizePortSpec converts the panel's port syntax ("80", "1000-11000",
// "80,443") into the one iptables speaks, and validates it. Returns the
// iptables-form spec and whether -m multiport is required.
func normalizePortSpec(portRange string) (string, bool, error) {
	spec := strings.ReplaceAll(portRange, "-", ":")
	multi := strings.ContainsAny(spec, ":,")

	entries := strings.Split(spec, ",")
	if len(entries) > maxMultiportEntries {
		return "", false, fmt.Errorf("too many port entries (iptables allows %d): %s", maxMultiportEntries, portRange)
	}
	for _, entry := range entries {
		bounds := strings.Split(entry, ":")
		if len(bounds) > 2 {
			return "", false, fmt.Errorf("invalid port range: %s", portRange)
		}
		prev := 0
		for _, b := range bounds {
			port, err := strconv.Atoi(b)
			if err != nil || port < 1 || port > 65535 {
				return "", false, fmt.Errorf("invalid port: %s", portRange)
			}
			// A range must ascend; "11000-1000" silently matches nothing in
			// iptables, which is worse than refusing it.
			if prev != 0 && port < prev {
				return "", false, fmt.Errorf("invalid port range: %s", portRange)
			}
			prev = port
		}
	}
	return spec, multi, nil
}

// SplitRuleLine splits an `iptables -S` line into its words, undoing the
// quoting iptables applies to values such as a log prefix with spaces.
func SplitRuleLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && inQuote && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == '"':
			inQuote, have = !inQuote, true
		case c == ' ' && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// LineSpec is the rulespec of an `iptables -S` "-A CHAIN ..." line, ready to
// pass to a delete; nil for anything else.
func LineSpec(line string) []string {
	f := SplitRuleLine(line)
	if len(f) < 3 || f[0] != "-A" {
		return nil
	}
	return f[2:]
}
