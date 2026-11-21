package netrules

import (
	"os"
	"path/filepath"
	"strings"
)

// Firewall enforcement states reported to the panel. An agent too old to report
// one sends nothing, which the panel shows as unknown.
const (
	FirewallEnforced = "enforced"
	FirewallOff      = "off"
)

// BridgeNFProcDir holds the bridge netfilter switches; it exists only while
// br_netfilter is loaded.
const BridgeNFProcDir = "/proc/sys/net/bridge"

// BridgeNFKeys must both be 1 for frames switched between ports of one bridge
// to reach iptables and ip6tables.
var BridgeNFKeys = []string{"bridge-nf-call-iptables", "bridge-nf-call-ip6tables"}

// BridgeNFState reports whether bridged guest traffic passes through the
// host's iptables rules. Without it a VM's firewall rules and the outbound scan
// guard are installed and filter nothing.
func BridgeNFState(procDir string) string {
	for _, key := range BridgeNFKeys {
		b, err := os.ReadFile(filepath.Join(procDir, key))
		if err != nil || strings.TrimSpace(string(b)) != "1" {
			return FirewallOff
		}
	}
	return FirewallEnforced
}

// PersistBridgeNetfilter writes the module and sysctl files under etcDir,
// leaving files that already say the same thing untouched. The module is
// persisted as well as the sysctl: a sysctl for a module that is not loaded yet
// fails silently at boot.
func PersistBridgeNetfilter(etcDir string) error {
	sysctl := ""
	for _, key := range BridgeNFKeys {
		sysctl += "net.bridge." + key + " = 1\n"
	}
	for path, content := range map[string]string{
		filepath.Join(etcDir, "modules-load.d", "maburvm.conf"):     "br_netfilter\n",
		filepath.Join(etcDir, "sysctl.d", "99-maburvm-bridge.conf"): sysctl,
	} {
		if have, err := os.ReadFile(path); err == nil && string(have) == content {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
