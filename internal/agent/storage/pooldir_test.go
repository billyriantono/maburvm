package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePoolDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const def = "/var/lib/libvirt/images"

	if got, err := ResolvePoolDir("", def); err != nil || got != def {
		t.Errorf("no pool: %q, %v", got, err)
	}
	if got, err := ResolvePoolDir(" "+dir+"/ ", def); err != nil || got != dir {
		t.Errorf("existing dir: %q, %v", got, err)
	}
	for _, bad := range []string{filepath.Join(dir, "missing"), file, "vg0", "relative/dir"} {
		if got, err := ResolvePoolDir(bad, def); err == nil {
			t.Errorf("%q must be refused, got %q", bad, got)
		}
	}
}
