package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolvePoolDir is the directory a new disk goes in: def when the panel named
// no pool, the pool when it is an existing directory, and an error otherwise.
// Falling back to def for a pool that is missing would put the disk on the
// root filesystem without anyone noticing. So would a shared pool that is not
// mounted here, which is refused the same way.
func ResolvePoolDir(poolPath, def string) (string, error) {
	p := strings.TrimSpace(poolPath)
	if p == "" {
		return def, nil
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("storage pool %q is not a directory path; VM disks need a directory pool", p)
	}
	if info, err := os.Stat(p); err != nil || !info.IsDir() {
		return "", fmt.Errorf("storage pool directory %s does not exist on this node; the disk is not put on the root filesystem instead. Create or mount the directory, or correct the pool's path", p)
	}
	p = filepath.Clean(p)
	// A shared pool that is not mounted is the local disk underneath.
	if err := CheckSharedDir(p); err != nil {
		return "", err
	}
	return p, nil
}
