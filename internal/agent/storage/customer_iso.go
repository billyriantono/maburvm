package storage

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// staleDownload is how long a partial download may sit unwritten before it is
// taken for abandoned. One attempt is cut off after half an hour.
const staleDownload = 2 * time.Hour

// CustomerISOName is the cache file name for an ISO URL a customer supplied:
// a hash of the whole URL, so two URLs never share a file, and a readable tail
// reduced to characters that are safe in a path whatever the customer typed.
func CustomerISOName(rawURL string) string {
	base := "image.iso"
	if u, err := url.Parse(rawURL); err == nil {
		if b := path.Base(u.Path); b != "." && b != "/" {
			base = b
		}
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, base)
	if len(base) > 80 {
		base = base[len(base)-80:] // keep the extension
	}
	// "<vm>-seed.iso" is a VM's own setup disc, deleted along with the VM. A
	// customer's file must never be taken for one: other VMs may have it attached.
	if strings.HasSuffix(base, "seed.iso") {
		base = strings.TrimSuffix(base, ".iso") + "_.iso"
	}
	sum := sha256.Sum256([]byte(rawURL))
	return fmt.Sprintf("%x-%s", sum[:8], base)
}

// PruneUnused removes the files directly in dir that inUse does not claim and
// that were last written or attached more than minAge ago, and returns the
// ones it removed. A partial download (a dot file) is left until it has been
// idle long enough to be abandoned, whatever minAge says.
//
// Only regular files in dir itself are ever removed: nothing is followed out
// of it and nothing beneath it is touched.
func PruneUnused(dir string, inUse func(path string) bool, minAge time.Duration) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		age := minAge
		if strings.HasPrefix(e.Name(), ".") {
			age = staleDownload
		}
		full := filepath.Join(dir, e.Name())
		if time.Since(info.ModTime()) < age || inUse(full) {
			continue
		}
		if os.Remove(full) == nil {
			removed = append(removed, full)
		}
	}
	return removed
}
