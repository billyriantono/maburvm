package netrules

import (
	"fmt"
	"net"
	"strings"
)

// HostSnapshot is the agent's rule chains as listed once per drift check.
type HostSnapshot struct {
	Firewall, Firewall6 []string // MABURVM-FIREWALL in iptables / ip6tables
	Postrouting         []string // nat POSTROUTING
	Forward             []string // filter FORWARD
	PortForwards        []string // nat MABURVM-NAT
	Have6               bool     // ip6tables is in use
	HaveEbtables        bool
	EbChains, EbJumps   map[string]bool // ebtables chains, and those FORWARD jumps to
}

// VMExpect is what should be on the host for one VM.
type VMExpect struct {
	Firewall VMFirewall
	// Primary is the address NAT and the FORWARD accepts are written for;
	// Masquerade says it is private and so gets a masquerade.
	Primary    string
	Masquerade bool
	// PortForwards is how many forwards should be installed; -1 when the node
	// has no address to publish them on and so installs none.
	PortForwards int
	// AntiSpoof: an ebtables chain must be in place (the VM runs, has the
	// feature on and an address on one of its taps).
	AntiSpoof bool
}

// Drift compares what should be installed for a VM with the snapshot and says
// what differs, in words for the log; nil when nothing does. A firewall that
// cannot be built is not compared: applying it fails the same way.
func Drift(e VMExpect, s HostSnapshot) []string {
	id := e.Firewall.VMID
	var out []string
	if v4, v6, err := BuildFirewall(e.Firewall, "a"); err == nil {
		tag := VMRuleTag(id)
		if n := CountTagged(s.Firewall, tag); n != v4.Len() {
			out = append(out, fmt.Sprintf("firewall has %d entries, want %d", n, v4.Len()))
		}
		if n := CountTagged(s.Firewall6, tag); s.Have6 && n != v6.Len() {
			out = append(out, fmt.Sprintf("IPv6 firewall has %d entries, want %d", n, v6.Len()))
		}
	}
	if e.Primary != "" {
		want := 0
		if e.Masquerade {
			want = 1
		}
		if n := CountComment(s.Postrouting, "maburvm-vm-"+id); n != want {
			out = append(out, fmt.Sprintf("NAT has %d masquerade entries, want %d", n, want))
		}
		if n := CountComment(s.Forward, "maburvm-vm-"+id+"-out") + CountComment(s.Forward, "maburvm-vm-"+id+"-in"); n != 2 {
			out = append(out, fmt.Sprintf("FORWARD has %d accept entries, want 2", n))
		}
	}
	if e.PortForwards >= 0 {
		if n := CountTagged(s.PortForwards, "maburvm-vm-"+id+"-port-"); n != e.PortForwards {
			out = append(out, fmt.Sprintf("%d port forwards installed, want %d", n, e.PortForwards))
		}
	}
	if e.AntiSpoof && s.HaveEbtables {
		in := false
		for _, c := range EbChainNames(id) {
			in = in || (s.EbChains[c] && s.EbJumps[c])
		}
		if !in {
			out = append(out, "anti-spoofing chain missing")
		}
	}
	return out
}

// CountTagged counts the lines that contain tag.
func CountTagged(lines []string, tag string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, tag) {
			n++
		}
	}
	return n
}

// CountComment counts the lines whose comment is exactly comment.
func CountComment(lines []string, comment string) int {
	n := 0
	for _, l := range lines {
		f := SplitRuleLine(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "--comment" && f[i+1] == comment {
				n++
				break
			}
		}
	}
	return n
}

// LineAddr returns the address a line matches with flag ("-s" or "-d"),
// without a /32.
func LineAddr(line, flag string) string {
	f := SplitRuleLine(line)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == flag {
			return strings.TrimSuffix(f[i+1], "/32")
		}
	}
	return ""
}

// EbChainNames are the two names a VM's ebtables chain alternates between, so
// a rewrite builds the new chain beside the old one. The first is the name
// earlier agents used. ebtables chain names are capped at 31 characters.
func EbChainNames(vmID string) [2]string {
	id := strings.ReplaceAll(vmID, "-", "")
	if len(id) > 20 {
		id = id[:20]
	}
	return [2]string{"MAS-" + id, "MAX-" + id}
}

// ParseEbtablesListing reads `ebtables -L` output: the chains that exist and
// the chains FORWARD jumps to.
func ParseEbtablesListing(out string) (chains, forwardJumps map[string]bool) {
	chains, forwardJumps = map[string]bool{}, map[string]bool{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Bridge chain: "); ok {
			current = strings.TrimSpace(strings.SplitN(rest, ",", 2)[0])
			chains[current] = true
			continue
		}
		if current != "FORWARD" {
			continue
		}
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-j" {
				forwardJumps[f[i+1]] = true
			}
		}
	}
	return chains, forwardJumps
}

// PortForwardKey identifies a port forward by what it does, from a rulespec as
// built or as `iptables -S` prints it, so the installed set can be compared
// with the wanted one without caring how iptables spelled it.
func PortForwardKey(spec []string) string {
	var dst, src, proto, dport, to, comment string
	for i := 0; i+1 < len(spec); i++ {
		v := spec[i+1]
		switch spec[i] {
		case "-d":
			dst = canonicalNet(v)
		case "-s":
			src = canonicalNet(v)
		case "-p":
			proto = v
		case "--dport":
			dport = v
		case "--to-destination":
			to = v
		case "--comment":
			comment = v
		default:
			continue
		}
		i++
	}
	return strings.Join([]string{dst, src, proto, dport, to, comment}, "|")
}

// canonicalNet writes an address or CIDR the way iptables prints it.
func canonicalNet(v string) string {
	if !strings.Contains(v, "/") {
		if ip := net.ParseIP(v); ip != nil && ip.To4() != nil {
			v += "/32"
		}
	}
	if _, n, err := net.ParseCIDR(v); err == nil {
		if n.String() == "0.0.0.0/0" {
			return ""
		}
		return n.String()
	}
	return v
}

// Guest is a defined domain as the post-reboot VPC restore sees it.
type Guest struct {
	ID        string
	Autostart bool
	Running   bool
	Bridges   []string
}

// VPCGuestsToStart picks the guests libvirt could not autostart at boot
// because their VPC bridge did not exist yet: set to autostart, not running,
// and on one of the bridges just rebuilt. Starting them is what libvirt would
// have done; a guest that is not set to autostart is left off.
func VPCGuestsToStart(guests []Guest, bridges map[string]bool) []string {
	var out []string
	for _, g := range guests {
		if !g.Autostart || g.Running {
			continue
		}
		for _, b := range g.Bridges {
			if bridges[b] {
				out = append(out, g.ID)
				break
			}
		}
	}
	return out
}
