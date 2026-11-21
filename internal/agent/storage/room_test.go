package storage

import "testing"

func TestHoldRoomSharesFreeSpace(t *testing.T) {
	const gib = int64(1) << 30
	// 100 GiB free, 20 reserved: one 60 GiB API copy fits, a second one does not.
	a, ok := HoldRoom("/vm", 100*gib, 60*gib, 20*gib, true)
	if !ok {
		t.Fatal("the first copy should fit")
	}
	if _, ok := HoldRoom("/vm", 100*gib, 60*gib, 20*gib, true); ok {
		t.Fatal("a second copy counted the same free space")
	}
	// An overlay export must leave the copy its room too.
	if _, ok := HoldRoom("/vm", 100*gib, 30*gib, 20*gib, false); ok {
		t.Fatal("an export counted the copy's room")
	}
	if c, ok := HoldRoom("/data", 100*gib, 60*gib, 20*gib, true); !ok {
		t.Fatal("another file system has its own room")
	} else {
		c()
	}
	if got := HeldRoom("/vm"); got != 60*gib {
		t.Fatalf("held %d, want %d", got, 60*gib)
	}
	a()
	a() // a second release gives nothing back twice
	if got := HeldRoom("/vm"); got != 0 {
		t.Fatalf("held %d after release, want 0", got)
	}

	// Overlay exports check against each other as they always did, but an API
	// copy must fit beside them.
	e1, ok1 := HoldRoom("/vm", 100*gib, 70*gib, 20*gib, false)
	e2, ok2 := HoldRoom("/vm", 100*gib, 70*gib, 20*gib, false)
	if !ok1 || !ok2 {
		t.Fatal("overlay exports refused each other")
	}
	if _, ok := HoldRoom("/vm", 200*gib, 60*gib, 20*gib, true); ok {
		t.Fatal("an API copy ignored the exports running")
	}
	e1()
	e2()
	if got := HeldRoom("/vm"); got != 0 {
		t.Fatalf("held %d after release, want 0", got)
	}
}
