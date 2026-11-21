package netrules

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxNullRoute is the longest a null-route may last. It always expires: a
// forgotten one would keep a customer's address dark with nobody remembering
// why.
const MaxNullRoute = 7 * 24 * time.Hour

// NullRoute is one address the node drops all traffic to until Until.
type NullRoute struct {
	IP     string
	Until  time.Time
	Reason string
}

// NullRouteIP checks that s names a single host address a null-route may
// cover and returns it in canonical form.
func NullRouteIP(s string) (ip string, v6 bool, err error) {
	parsed := net.ParseIP(strings.TrimSpace(s))
	if parsed == nil {
		return "", false, fmt.Errorf("%q is not an IP address", s)
	}
	if parsed.IsUnspecified() || parsed.IsLoopback() || parsed.IsMulticast() || parsed.IsLinkLocalUnicast() {
		return "", false, fmt.Errorf("%s cannot be null-routed", parsed)
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String(), false, nil
	}
	return parsed.String(), true, nil
}

// ParseNullRoutes reads the node's null-route file: "<ip> <unix expiry>",
// optionally followed by "# reason". Entries already expired at now, and lines
// that do not parse, are left out; expired counts the former so the caller
// knows to rewrite the file and lift them.
func ParseNullRoutes(content string, now time.Time) (active []NullRoute, expired int) {
	seen := map[string]int{}
	for _, line := range strings.Split(content, "\n") {
		body, reason, _ := strings.Cut(line, "#")
		fields := strings.Fields(body)
		if len(fields) != 2 {
			continue
		}
		ip, _, err := NullRouteIP(fields[0])
		if err != nil {
			continue
		}
		unix, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		until := time.Unix(unix, 0).UTC()
		if !until.After(now) {
			expired++
			continue
		}
		r := NullRoute{IP: ip, Until: until, Reason: strings.TrimSpace(reason)}
		if i, dup := seen[ip]; dup {
			active[i] = r // the later line wins, as a hand edit appended at the end would expect
			continue
		}
		seen[ip] = len(active)
		active = append(active, r)
	}
	return active, expired
}

// FormatNullRoutes renders the file, ordered by address so a rewrite does not
// shuffle lines an operator is reading.
func FormatNullRoutes(routes []NullRoute) string {
	sorted := append([]NullRoute(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].IP < sorted[j].IP })
	var b strings.Builder
	b.WriteString("# Addresses whose traffic this node drops until the time given (unix seconds).\n")
	b.WriteString("# Managed by the panel; entries lift themselves when they expire.\n")
	for _, r := range sorted {
		fmt.Fprintf(&b, "%s %d", r.IP, r.Until.Unix())
		if reason := oneLine(r.Reason); reason != "" {
			fmt.Fprintf(&b, "   # %s", reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// SetNullRoute adds (or extends) or lifts one address in a list.
func SetNullRoute(routes []NullRoute, ip string, until time.Time, reason string, on bool) []NullRoute {
	out := make([]NullRoute, 0, len(routes)+1)
	for _, r := range routes {
		if r.IP != ip {
			out = append(out, r)
		}
	}
	if on {
		out = append(out, NullRoute{IP: ip, Until: until.UTC(), Reason: oneLine(reason)})
	}
	return out
}

// NullRouteRule drops everything sent to ip.
func NullRouteRule(ip string) []string {
	return []string{"-d", ip, "-j", "DROP", "-m", "comment", "--comment", "maburvm-nullroute"}
}

// oneLine keeps a reason to one short line: it is written into a file an
// operator edits, where a newline or "#" would forge or cut entries.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '#' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
