package ceph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// A VM with a disk on RBD does not start with the node through libvirt:
// libvirt would start it at boot before anything could check that no other
// node runs it on the same images (one recovered elsewhere while this node
// was down), and two QEMUs writing one image corrupt it. Its start-with-node
// flag is kept here instead, and the agent starts it once the node has booted,
// through the start that checks the images are free.

// StartsFile holds the flags; the agent points it into its data directory.
var StartsFile = "/var/lib/maburvm/rbd-autostart.json"

// BootID is the node's boot id; tests replace it.
var BootID = func() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

type starts struct {
	BootID string   `json:"boot_id"`
	VMs    []string `json:"vms"`
}

var startsMu sync.Mutex

func loadStarts() starts {
	var s starts
	if b, err := os.ReadFile(StartsFile); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveStarts(s starts) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(StartsFile), 0o700); err != nil {
		return err
	}
	tmp := StartsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StartsFile)
}

// SetStartsWithNode records whether the VM starts when the node boots. Nothing
// is written when that does not change, so VMs not on RBD never touch the file.
func SetStartsWithNode(vmID string, on bool) error {
	startsMu.Lock()
	defer startsMu.Unlock()
	s := loadStarts()
	if slices.Contains(s.VMs, vmID) == on {
		return nil
	}
	if s.BootID == "" {
		s.BootID = BootID()
	}
	s.VMs = slices.DeleteFunc(s.VMs, func(v string) bool { return v == vmID })
	if on {
		s.VMs = append(s.VMs, vmID)
	}
	return saveStarts(s)
}

// StartsAfterBoot returns the VMs that start with the node when it has booted
// since the list was last stamped, and stamps it with this boot; nil when it
// has not, so an agent restart starts nothing.
func StartsAfterBoot() []string {
	startsMu.Lock()
	defer startsMu.Unlock()
	s := loadStarts()
	now := BootID()
	if now == "" || s.BootID == "" || s.BootID == now {
		return nil
	}
	s.BootID = now
	_ = saveStarts(s)
	return s.VMs
}
