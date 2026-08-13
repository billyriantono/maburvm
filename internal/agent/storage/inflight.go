package storage

import (
	"os"
	"sync"
	"time"
)

// InFlightExport is one disk export currently running on this node.
type InFlightExport struct {
	VMID string
	// Kind is "backup" or "image"; the same export path serves both.
	Kind        string
	SourcePath  string
	DestPath    string
	SourceBytes int64
	StartedAt   time.Time
}

// exports is the node's registry of running exports.
//
// It exists because a compressed export of a large disk runs for hours and,
// until now, produced no observable signal at all: the panel showed "pending",
// which was indistinguishable from a job that had never started and from one
// that was stuck. Operators had to SSH to the node and look for a qemu-img
// process to find out which.
//
// In memory rather than persisted on purpose. An export cannot survive the
// process that spawned it — an agent restart kills the RPC that would have
// delivered the result — so a record outliving the process would only ever
// describe work that is no longer happening.
var exports = struct {
	sync.RWMutex
	byVM map[string]*InFlightExport
}{byVM: map[string]*InFlightExport{}}

// TrackExport registers an export as running and returns a function to call when
// it finishes, however it finishes.
func TrackExport(vmID, kind, sourcePath, destPath string, sourceBytes int64) (done func()) {
	entry := &InFlightExport{
		VMID:        vmID,
		Kind:        kind,
		SourcePath:  sourcePath,
		DestPath:    destPath,
		SourceBytes: sourceBytes,
		StartedAt:   time.Now(),
	}

	exports.Lock()
	exports.byVM[vmID] = entry
	exports.Unlock()

	return func() {
		exports.Lock()
		delete(exports.byVM, vmID)
		exports.Unlock()
	}
}

// ListExports reports the exports running now, with the bytes each has written.
//
// The written size is read from the output file at call time rather than
// tracked, because qemu-img gives no progress callback and the file's growth is
// the only honest signal available.
func ListExports() []InFlightExport {
	exports.RLock()
	defer exports.RUnlock()

	out := make([]InFlightExport, 0, len(exports.byVM))
	for _, e := range exports.byVM {
		out = append(out, *e)
	}
	return out
}

// WrittenBytes returns how much an export has produced so far, or 0 when the
// output file does not exist yet.
func WrittenBytes(destPath string) int64 {
	info, err := os.Stat(destPath)
	if err != nil {
		return 0
	}
	return info.Size()
}
