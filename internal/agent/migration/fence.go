package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Fenced VMs: VMs the panel recovered onto another node while this one was
// down. Their copies here must never run again: the same disk (on shared
// storage) or the same address would be in use twice. The panel sends the
// whole list when the node comes back; it is kept on disk so a later reboot
// still knows it before the panel calls.

// FenceFile holds the list. The agent sets it to a path in its data
// directory; empty means no VM is fenced.
var FenceFile string

var fenceMu sync.Mutex

func readFenced() map[string]bool {
	out := map[string]bool{}
	if FenceFile == "" {
		return out
	}
	b, err := os.ReadFile(FenceFile)
	if err != nil {
		return out
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func writeFenced(set map[string]bool) error {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(FenceFile), 0o700); err != nil {
		return err
	}
	tmp := FenceFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, FenceFile)
}

// IsFenced reports a VM that lives on another node now and must not run here.
func IsFenced(vmID string) bool {
	fenceMu.Lock()
	defer fenceMu.Unlock()
	return readFenced()[vmID]
}

// SetFenced replaces the list. Ids that are not UUIDs are left out.
func SetFenced(ids []string) error {
	fenceMu.Lock()
	defer fenceMu.Unlock()
	set := map[string]bool{}
	for _, id := range ids {
		if ValidUUID(id) {
			set[id] = true
		}
	}
	return writeFenced(set)
}

// Unfence takes one VM off the list, once its copy here is gone.
func Unfence(vmID string) error {
	fenceMu.Lock()
	defer fenceMu.Unlock()
	set := readFenced()
	if !set[vmID] {
		return nil
	}
	delete(set, vmID)
	return writeFenced(set)
}
