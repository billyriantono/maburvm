package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvictLRU(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, size int, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldest := write("0123456789ab-debian-12.qcow2", 100, 30*24*time.Hour)
	attached := write("abcdef012345-rescue.iso", 100, 20*24*time.Hour)
	older := write("img-0123456789abcdef.qcow2", 100, 10*24*time.Hour)
	recent := write("fedcba987654-windows.qcow2", 100, time.Hour)
	partial := write("00112233aabb-new.qcow2.part", 100, time.Minute)
	// Built on the node by hand: older than everything, never a download.
	handMade := write("windows-2022.qcow2", 5000, 90*24*time.Hour)
	if err := os.MkdirAll(filepath.Join(dir, "customer-iso"), 0o755); err != nil {
		t.Fatal(err)
	}
	inSub := filepath.Join(dir, "customer-iso", "0123456789ab-x.iso")
	if err := os.WriteFile(inSub, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	asked := map[string]bool{}
	removed, left := EvictLRU(dir, 250, 6*time.Hour, func(p string) bool { asked[p] = true; return p == attached })

	// Oldest download first, skipping what a VM uses, until the cap is met.
	if len(removed) != 2 || removed[0] != oldest || removed[1] != older {
		t.Fatalf("removed %v", removed)
	}
	if left != 300 {
		t.Fatalf("left %d, want 300: what remains is in use or recent, and hand-placed files do not count", left)
	}
	for _, p := range []string{attached, recent, partial, inSub, handMade} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	if asked[recent] || asked[partial] || asked[handMade] {
		t.Error("recent downloads and hand-placed files need no check against the VMs")
	}

	// Under the cap nothing goes.
	if removed, _ := EvictLRU(dir, 1<<30, 0, func(string) bool { return false }); len(removed) != 0 {
		t.Fatalf("removed %v under the cap", removed)
	}
	// A cap of nothing still leaves every file the agent did not download.
	EvictLRU(dir, 0, 0, func(string) bool { return false })
	if _, err := os.Stat(handMade); err != nil {
		t.Fatal("a template placed by hand was removed")
	}
	// A directory that is not there is nothing to do.
	if removed, left := EvictLRU(filepath.Join(dir, "missing"), 0, 0, func(string) bool { return false }); removed != nil || left != 0 {
		t.Fatal("a missing directory must be a no-op")
	}
}

func TestIsCacheDownload(t *testing.T) {
	for name, want := range map[string]bool{
		"0123456789ab-ubuntu-24.04.qcow2":     true,
		"0123456789ab-image.qcow2.part":       true,
		"img-0123456789abcdef.qcow2":          true,
		"img-0123456789abcdef.qcow2.part":     true,
		"clone-3f2c1b1e-aaaa-bbbb-cccc.qcow2": true,
		"windows-2022.qcow2":                  false,
		"rescue.iso":                          false,
		"0123456789AB-upper-hex.qcow2":        false,
		"img-short.qcow2":                     false,
		"debian-12-generic-amd64.qcow2":       false,
	} {
		if got := IsCacheDownload(name); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}
