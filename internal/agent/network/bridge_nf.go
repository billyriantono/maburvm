package network

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coreos/go-iptables/iptables"

	"github.com/maburvm/panel/internal/agent/netrules"
)

// bridgeNetfilterEnv set to "enforce" lets the agent turn bridge netfilter on.
// Unset, the agent only reports the state: on a node with running guests,
// turning it on starts enforcing rules that have never applied to them, and
// when that happens is the operator's call, not an agent restart's.
const bridgeNetfilterEnv = "MABURVM_BRIDGE_NETFILTER"

// FirewallEnforcement reports whether bridged guest traffic passes through the
// host's iptables rules right now.
func FirewallEnforcement() string { return netrules.BridgeNFState(netrules.BridgeNFProcDir) }

// ensureBridgeNetfilter makes enforcement that is on survive a reboot, and
// turns it on when the operator asked for that. Without it per-VM firewall
// rules and the scan guard are installed and filter nothing for bridged guests.
func ensureBridgeNetfilter() {
	if FirewallEnforcement() != netrules.FirewallEnforced {
		if os.Getenv(bridgeNetfilterEnv) != "enforce" {
			log.Printf("[NetworkManager] WARNING: bridge netfilter is off: per-VM firewall rules and the scan guard are NOT filtering bridged traffic on this node. "+
				"Left as it is, because turning it on starts enforcing rules on VMs that are already running. "+
				"Set %s=enforce and restart the agent to turn it on", bridgeNetfilterEnv)
			return
		}
		// With a DROP policy, turning this on would cut every bridged guest the
		// agent has no accept rule for.
		if forwardPolicyDrops() {
			log.Printf("[NetworkManager] WARNING: bridge netfilter is off and the FORWARD policy is DROP: not turning it on, " +
				"because guests without their own accept rule would lose their network. Per-VM firewall rules and the scan guard are NOT filtering bridged traffic")
			return
		}
		if out, err := exec.Command("modprobe", "br_netfilter").CombinedOutput(); err != nil {
			log.Printf("[NetworkManager] WARNING: could not load br_netfilter (%v: %s)", err, strings.TrimSpace(string(out)))
		}
		for _, key := range netrules.BridgeNFKeys {
			if err := os.WriteFile(filepath.Join(netrules.BridgeNFProcDir, key), []byte("1\n"), 0o644); err != nil {
				log.Printf("[NetworkManager] WARNING: could not set %s: %v", key, err)
			}
		}
		if FirewallEnforcement() != netrules.FirewallEnforced {
			log.Printf("[NetworkManager] WARNING: bridge netfilter could not be turned on: per-VM firewall rules and the scan guard are NOT filtering bridged traffic on this node")
			return
		}
		log.Printf("[NetworkManager] bridge netfilter turned on: per-VM firewall rules and the scan guard now filter bridged traffic")
	}
	if err := netrules.PersistBridgeNetfilter("/etc"); err != nil {
		log.Printf("[NetworkManager] WARNING: bridge netfilter is on but not persisted, a reboot may turn it off: %v", err)
	}
}

// forwardPolicyDrops reports whether the filter FORWARD chain of either
// family drops by default.
func forwardPolicyDrops() bool {
	for _, family := range []iptables.Protocol{iptables.ProtocolIPv4, iptables.ProtocolIPv6} {
		ipt, err := iptables.New(iptables.IPFamily(family))
		if err != nil {
			continue
		}
		if rules, err := ipt.List(FilterTable, ForwardChain); err == nil && len(rules) > 0 && strings.TrimSpace(rules[0]) == "-P FORWARD DROP" {
			return true
		}
	}
	return false
}
