package ceph

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A VM on RBD starts with the node through the agent, once per boot, and
// never on an agent restart; VMs not on RBD leave no file behind.
func TestStartsWithNodeOncePerBoot(t *testing.T) {
	StartsFile = filepath.Join(t.TempDir(), "rbd-autostart.json")
	boot := "boot-1"
	BootID = func() string { return boot }

	if err := SetStartsWithNode("local-vm", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StartsFile); !os.IsNotExist(err) {
		t.Fatalf("a VM not on RBD wrote the file: %v", err)
	}
	for _, id := range []string{"a", "b", "a"} {
		if err := SetStartsWithNode(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := StartsAfterBoot(); got != nil {
		t.Fatalf("same boot (an agent restart) starts %v", got)
	}
	boot = "boot-2"
	if got := StartsAfterBoot(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("after a reboot: %v", got)
	}
	if got := StartsAfterBoot(); got != nil {
		t.Fatalf("a second look in the same boot starts %v", got)
	}
	if err := SetStartsWithNode("a", false); err != nil {
		t.Fatal(err)
	}
	boot = "boot-3"
	if got := StartsAfterBoot(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("after a's flag was cleared: %v", got)
	}
	// An emptied list is stamped too: a VM flagged later in this boot must
	// not start on the next agent restart.
	if err := SetStartsWithNode("b", false); err != nil {
		t.Fatal(err)
	}
	boot = "boot-4"
	StartsAfterBoot()
	if err := SetStartsWithNode("c", true); err != nil {
		t.Fatal(err)
	}
	if got := StartsAfterBoot(); got != nil {
		t.Fatalf("an agent restart starts %v", got)
	}
}
