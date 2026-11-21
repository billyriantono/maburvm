package migration

import (
	"path/filepath"
	"testing"
)

// The fence list survives an agent restart (it is read from disk each time),
// is replaced as a whole, and drops what is not a VM id.
func TestFenceListIsKeptAndReplaced(t *testing.T) {
	prev := FenceFile
	t.Cleanup(func() { FenceFile = prev })
	const a, b = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

	FenceFile = ""
	if IsFenced(a) {
		t.Fatal("no file, no fenced VM")
	}
	FenceFile = filepath.Join(t.TempDir(), "sub", "fenced-vms.json")
	if IsFenced(a) {
		t.Fatal("a missing file fences nothing")
	}
	if err := SetFenced([]string{a, "../etc/passwd", b}); err != nil {
		t.Fatal(err)
	}
	if !IsFenced(a) || !IsFenced(b) || IsFenced("../etc/passwd") {
		t.Fatal("both VMs are fenced, and nothing else")
	}
	if err := SetFenced([]string{b}); err != nil {
		t.Fatal(err)
	}
	if IsFenced(a) || !IsFenced(b) {
		t.Fatal("the list is replaced as a whole")
	}
	if err := Unfence(b); err != nil {
		t.Fatal(err)
	}
	if IsFenced(b) {
		t.Fatal("a removed copy is no longer fenced")
	}
}
