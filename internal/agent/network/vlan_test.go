package network

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func newTestVLAN(t *testing.T, bridges string, filtering bool) (*VLANManager, *[]string) {
	t.Helper()
	var ran []string
	m := &VLANManager{
		bridges:     parseVLANBridges(bridges),
		assignments: map[string]int{},
		statePath:   filepath.Join(t.TempDir(), "vlan.json"),
		lastErr:     map[string]string{},
		run: func(name string, args ...string) error {
			ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
			return nil
		},
		nics:      func(string) ([]NIC, error) { return []NIC{{Tap: "vnet7", Bridge: "br1"}}, nil },
		filtering: func(string) bool { return filtering },
	}
	return m, &ran
}

func TestParseVLANBridges(t *testing.T) {
	got := parseVLANBridges(" br1:bond0 , br2:eth3,br3, :eth9,")
	if !reflect.DeepEqual(got, map[string]string{"br1": "bond0", "br2": "eth3"}) {
		t.Fatalf("got %v", got)
	}
	if len(parseVLANBridges("")) != 0 {
		t.Fatal("empty spec must enable nothing")
	}
}

func TestAssignVLANOnAnEnabledBridge(t *testing.T) {
	m, ran := newTestVLAN(t, "br1:bond0", false)
	if err := m.AssignVLAN("vm-1", 120); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ip link set dev br1 type bridge vlan_filtering 1",
		"bridge vlan add dev vnet7 vid 120 pvid untagged", // joins its VLAN first
		"bridge vlan del dev vnet7 vid 1",                 // then leaves the native one
		"bridge vlan add dev bond0 vid 120",               // the uplink carries it tagged
	}
	if !reflect.DeepEqual(*ran, want) {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(*ran, "\n"), strings.Join(want, "\n"))
	}

	// A fresh manager reads the assignment back from disk (agent restart).
	again := NewVLANManagerAt(m.statePath)
	if again.assignments["vm-1"] != 120 {
		t.Fatalf("assignment not saved: %v", again.assignments)
	}
}

func TestVLANIsRefusedOnABridgeThatIsNotEnabled(t *testing.T) {
	m, ran := newTestVLAN(t, "", false)
	err := m.AssignVLAN("vm-1", 120)
	if err == nil || !strings.Contains(err.Error(), VLANBridgesEnv) {
		t.Fatalf("want a refusal naming %s, got %v", VLANBridgesEnv, err)
	}
	if len(*ran) != 0 {
		t.Fatalf("touched the bridge anyway: %v", *ran)
	}
}

func TestRemoveVLANGoesBackToNative(t *testing.T) {
	m, ran := newTestVLAN(t, "br1:bond0", true)
	_ = m.AssignVLAN("vm-1", 120)
	*ran = nil
	if err := m.RemoveVLAN("vm-1", 0); err != nil {
		t.Fatal(err)
	}
	want := []string{"bridge vlan add dev vnet7 vid 1 pvid untagged", "bridge vlan del dev vnet7 vid 120"}
	if !reflect.DeepEqual(*ran, want) || m.Has("vm-1") {
		t.Fatalf("ran %v, still assigned=%v", *ran, m.Has("vm-1"))
	}
	*ran = nil
	if err := m.RemoveVLAN("vm-1", 0); err != nil || len(*ran) != 0 {
		t.Fatal("removing a VLAN that is not there must do nothing")
	}
}

func TestStoppedVMKeepsItsVLANOnRecord(t *testing.T) {
	m, ran := newTestVLAN(t, "br1:bond0", true)
	m.nics = func(string) ([]NIC, error) { return nil, errors.New("domain is not running") }
	if err := m.AssignVLAN("vm-1", 120); err == nil {
		t.Fatal("want an error while the VM has no tap")
	}
	if !m.Has("vm-1") {
		t.Fatal("the VLAN must stay on record for the reconciler")
	}
	// The VM starts: the reconciler puts its new tap into the VLAN.
	m.nics = func(string) ([]NIC, error) { return []NIC{{Tap: "vnet9", Bridge: "br1"}}, nil }
	*ran = nil
	m.Reconcile()
	if len(*ran) != 3 || !strings.Contains((*ran)[0], "vnet9 vid 120 pvid untagged") {
		t.Fatalf("reconcile ran %v", *ran)
	}
	// CleanupVM (full re-apply) forgets the record without touching the tap.
	*ran = nil
	_ = m.CleanupVM("vm-1")
	if m.Has("vm-1") || len(*ran) != 0 {
		t.Fatalf("cleanup ran %v", *ran)
	}
}
