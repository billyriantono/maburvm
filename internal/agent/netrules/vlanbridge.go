package netrules

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
)

// VLANBridgeState is how a bridge named for per-VM VLANs looks on the host.
type VLANBridgeState struct {
	Bridge, Uplink string
	// Problem says why VLANs cannot work on it now; empty when they can.
	Problem string
	// Filtering is the bridge's vlan_filtering flag, switched on by the agent
	// when a VM on it first gets a VLAN.
	Filtering bool
}

// CheckVLANBridge looks at a bridge and its uplink under sys (/sys/class/net).
func CheckVLANBridge(sys, bridge, uplink string) VLANBridgeState {
	st := VLANBridgeState{Bridge: bridge, Uplink: uplink}
	if !models.ValidInterfaceName(bridge) || !models.ValidInterfaceName(uplink) {
		st.Problem = "not a valid interface name"
		return st
	}
	b, err := os.ReadFile(filepath.Join(sys, bridge, "bridge", "vlan_filtering"))
	st.Filtering = err == nil && strings.TrimSpace(string(b)) == "1"
	switch {
	case !exists(filepath.Join(sys, bridge)):
		st.Problem = bridge + " does not exist on this node"
	case !exists(filepath.Join(sys, bridge, "bridge")):
		st.Problem = bridge + " is not a bridge"
	case !exists(filepath.Join(sys, uplink)):
		st.Problem = "the uplink " + uplink + " does not exist on this node"
	case !exists(filepath.Join(sys, bridge, "brif", uplink)):
		st.Problem = uplink + " is not a port of " + bridge
	}
	return st
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
