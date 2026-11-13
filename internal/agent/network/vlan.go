package network

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// IPPath is the path to the ip binary
	IPPath = "/sbin/ip"
	// BridgePath is the path to the bridge binary (for bridge vlan commands)
	BridgePath = "/sbin/bridge"
	// VLANMaxID is the maximum valid VLAN ID (12 bits = 4094, 0 and 4095 are reserved)
	VLANMaxID = 4094

	// VLANBridgesEnv lists the bridges on this node that may carry per-VM
	// VLANs, each with its uplink port: "br1:bond0,br2:eth3".
	VLANBridgesEnv = "MABURVM_VLAN_BRIDGES"
)

// VLANs on a Linux bridge work through the bridge's own VLAN table: the VM's
// tap becomes an untagged member of its VLAN, the uplink carries that VLAN
// tagged, and the bridge has VLAN filtering switched on so the table is
// actually enforced. (libvirt's <vlan> element is for Open vSwitch; on a Linux
// bridge it stops the VM from starting.)
//
// Switching filtering on changes how the whole bridge forwards: untagged
// traffic carries on, but tagged frames that used to pass straight through are
// dropped until their VLAN is added. So it is never done implicitly. An
// operator names the bridges in MABURVM_VLAN_BRIDGES; on any other bridge a
// VLAN request is refused with a message that says why.

// VLANManager assigns VLANs to VM taps and keeps them assigned.
type VLANManager struct {
	mu sync.Mutex
	// bridges maps an opted-in bridge to its uplink port.
	bridges map[string]string
	// assignments is vmID -> VLAN ID, saved to statePath so it survives an
	// agent restart; a tap is recreated plain whenever its VM restarts.
	assignments map[string]int
	statePath   string
	// lastErr remembers the last failure per VM so the reconciler logs a
	// problem once, not every 30 seconds.
	lastErr map[string]string

	run  func(name string, args ...string) error
	nics func(vmID string) ([]NIC, error)
	// filtering reports whether a bridge has VLAN filtering on.
	filtering func(bridge string) bool
}

// parseVLANBridges reads "br1:bond0,br2:eth3" into bridge -> uplink. Entries
// without an uplink are dropped: without it a VLAN could never leave the node.
func parseVLANBridges(spec string) map[string]string {
	out := map[string]string{}
	for _, entry := range strings.Split(spec, ",") {
		bridge, uplink, ok := strings.Cut(strings.TrimSpace(entry), ":")
		bridge, uplink = strings.TrimSpace(bridge), strings.TrimSpace(uplink)
		if !ok || bridge == "" || uplink == "" {
			if strings.TrimSpace(entry) != "" {
				log.Printf("[VLAN] ignoring %q in %s: write it as bridge:uplink", entry, VLANBridgesEnv)
			}
			continue
		}
		out[bridge] = uplink
	}
	return out
}

func vlanStatePath() string { return "/var/lib/maburvm/vlan-assignments.json" }

// NewVLANManager creates a VLANManager from the node's environment and the
// saved assignments.
func NewVLANManager() *VLANManager { return NewVLANManagerAt(vlanStatePath()) }

// NewVLANManagerAt is NewVLANManager with the state file named by the caller.
func NewVLANManagerAt(statePath string) *VLANManager {
	m := &VLANManager{
		bridges:     parseVLANBridges(os.Getenv(VLANBridgesEnv)),
		assignments: map[string]int{},
		statePath:   statePath,
		lastErr:     map[string]string{},
		run: func(name string, args ...string) error {
			if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
				return fmt.Errorf("%s %s: %v: %s", filepath.Base(name), strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		nics: vmNICs,
		filtering: func(bridge string) bool {
			b, err := os.ReadFile("/sys/class/net/" + bridge + "/bridge/vlan_filtering")
			return err == nil && strings.TrimSpace(string(b)) == "1"
		},
	}
	if b, err := os.ReadFile(m.statePath); err == nil {
		if err := json.Unmarshal(b, &m.assignments); err != nil {
			log.Printf("[VLAN] WARNING: ignoring unreadable %s: %v", m.statePath, err)
			m.assignments = map[string]int{}
		}
	}
	return m
}

func (m *VLANManager) save() {
	b, err := json.MarshalIndent(m.assignments, "", "  ")
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(m.statePath), 0o700); err == nil {
			tmp := m.statePath + ".tmp"
			if err = os.WriteFile(tmp, b, 0o600); err == nil {
				err = os.Rename(tmp, m.statePath)
			}
		}
	}
	if err != nil {
		log.Printf("[VLAN] WARNING: could not save assignments: %v", err)
	}
}

// primaryPort returns the tap and bridge of the VM's first bridged NIC.
func (m *VLANManager) primaryPort(vmID string) (tap, bridge string, err error) {
	nics, err := m.nics(vmID)
	if err != nil {
		return "", "", err
	}
	for _, n := range nics {
		if n.Tap != "" && n.Bridge != "" {
			return n.Tap, n.Bridge, nil
		}
	}
	return "", "", fmt.Errorf("VM %s has no network port right now (it is probably stopped)", vmID)
}

// vlanCommands is what puts tap into VLAN vid on its bridge. The order
// matters: the tap joins its VLAN before it leaves the native one, so it is
// never in neither.
func vlanCommands(tap, uplink string, vid int) [][]string {
	v := strconv.Itoa(vid)
	return [][]string{
		{"vlan", "add", "dev", tap, "vid", v, "pvid", "untagged"},
		{"vlan", "del", "dev", tap, "vid", "1"},
		{"vlan", "add", "dev", uplink, "vid", v},
	}
}

// apply puts the VM's current tap into its VLAN. Safe to repeat.
func (m *VLANManager) apply(vmID string, vid int) error {
	tap, bridge, err := m.primaryPort(vmID)
	if err != nil {
		return err
	}
	uplink, ok := m.bridges[bridge]
	if !ok {
		return fmt.Errorf("VLANs are not enabled on bridge %s on this node; add %s:<uplink> to %s in the agent's environment", bridge, bridge, VLANBridgesEnv)
	}
	if !m.filtering(bridge) {
		log.Printf("[VLAN] enabling VLAN filtering on %s (listed in %s). Untagged traffic is unaffected; tagged frames now need their VLAN added to pass.", bridge, VLANBridgesEnv)
		if err := m.run(IPPath, "link", "set", "dev", bridge, "type", "bridge", "vlan_filtering", "1"); err != nil {
			return err
		}
	}
	for i, args := range vlanCommands(tap, uplink, vid) {
		if err := m.run(BridgePath, args...); err != nil {
			if i == 1 {
				continue // the tap was already out of the native VLAN
			}
			return err
		}
	}
	return nil
}

// AssignVLAN records the VM's VLAN and applies it to its tap. The record is
// kept even when applying fails (a stopped VM has no tap), so Reconcile
// applies it as soon as it can.
func (m *VLANManager) AssignVLAN(vmID string, vlanID int) error {
	if vlanID < 1 || vlanID > VLANMaxID {
		return fmt.Errorf("invalid VLAN ID: %d (must be 1-%d)", vlanID, VLANMaxID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignments[vmID] != vlanID {
		m.assignments[vmID] = vlanID
		m.save()
	}
	delete(m.lastErr, vmID)
	return m.apply(vmID, vlanID)
}

// RemoveVLAN takes the VM out of its VLAN and back onto the native network.
// The vlanID argument is the one to remove; 0 means "whatever it has".
func (m *VLANManager) RemoveVLAN(vmID string, vlanID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	had, ok := m.assignments[vmID]
	if !ok {
		return nil
	}
	if vlanID == 0 {
		vlanID = had
	}
	delete(m.assignments, vmID)
	delete(m.lastErr, vmID)
	m.save()

	tap, bridge, err := m.primaryPort(vmID)
	if err != nil {
		return nil // no tap: nothing on the bridge to undo
	}
	if _, enabled := m.bridges[bridge]; !enabled {
		return nil
	}
	// Back into the native VLAN first, then out of the old one.
	if err := m.run(BridgePath, "vlan", "add", "dev", tap, "vid", "1", "pvid", "untagged"); err != nil {
		return err
	}
	_ = m.run(BridgePath, "vlan", "del", "dev", tap, "vid", strconv.Itoa(vlanID))
	return nil
}

// CleanupVM forgets the VM's VLAN. It does not touch the tap: this runs at the
// start of every full network re-apply, and pulling the tap back onto the
// native network for a moment would leak the VM's traffic there. A VM that is
// being deleted takes its tap with it.
func (m *VLANManager) CleanupVM(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.assignments[vmID]; ok {
		delete(m.assignments, vmID)
		delete(m.lastErr, vmID)
		m.save()
	}
	return nil
}

// Has reports whether the VM has a VLAN on record.
func (m *VLANManager) Has(vmID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.assignments[vmID]
	return ok
}

// Reconcile re-applies every recorded VLAN. A tap is created fresh, in the
// native VLAN, each time its VM starts, and the bridge can lose its filtering
// flag when the host's network config is re-applied; both are put right here.
func (m *VLANManager) Reconcile() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for vmID, vid := range m.assignments {
		err := m.apply(vmID, vid)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != m.lastErr[vmID] {
			if err != nil {
				log.Printf("[VLAN] VM %s is not in VLAN %d yet: %v", vmID, vid, err)
			} else {
				log.Printf("[VLAN] VM %s is in VLAN %d", vmID, vid)
			}
			m.lastErr[vmID] = msg
		}
	}
}
