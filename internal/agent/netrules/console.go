package netrules

import (
	"regexp"
	"slices"
	"strconv"
)

// ConsoleChain holds one rule per console port that is still bound to more
// than loopback, and is jumped to from INPUT.
const ConsoleChain = "MABURVM-CONSOLE"

// ConsoleShieldRule closes a console port to everything but the node itself.
// The panel reaches consoles through the agent, which connects over loopback.
func ConsoleShieldRule(port int) []string {
	return []string{"!", "-i", "lo", "-p", "tcp", "--dport", strconv.Itoa(port), "-j", "DROP"}
}

var shieldPortRE = regexp.MustCompile(`--dport (\d+)`)

// ShieldedPorts reads the ports out of the chain's rules as iptables lists them.
func ShieldedPorts(rules []string) []int {
	var ports []int
	for _, rule := range rules {
		if m := shieldPortRE.FindStringSubmatch(rule); m != nil {
			port, _ := strconv.Atoi(m[1])
			ports = append(ports, port)
		}
	}
	return ports
}

// ConsoleShieldPlan says which ports to close and which to open again. Adding
// before removing, instead of rebuilding the chain, leaves no moment in which
// a port that should stay closed is open.
func ConsoleShieldPlan(have, want []int) (add, remove []int) {
	for _, port := range want {
		if !slices.Contains(have, port) && !slices.Contains(add, port) {
			add = append(add, port)
		}
	}
	for _, port := range have {
		if !slices.Contains(want, port) && !slices.Contains(remove, port) {
			remove = append(remove, port)
		}
	}
	return add, remove
}
