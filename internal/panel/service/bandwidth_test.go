package service

import (
	"log/slog"
	"testing"
)

// calculateDelta is the core of bandwidth accounting: it turns cumulative,
// monotonically-increasing counters into per-interval deltas, and must treat a
// counter going backwards (VM reboot → counters reset to 0) as a fresh start
// rather than a huge negative delta.
func TestBandwidthCalculateDelta(t *testing.T) {
	s := NewBandwidthService(nil, slog.Default())
	const vm = "vm-1"

	// First report establishes the baseline and yields no usage.
	if rx, tx := s.calculateDelta(vm, 1000, 2000); rx != 0 || tx != 0 {
		t.Fatalf("first report: want (0,0), got (%d,%d)", rx, tx)
	}

	// Normal increase → delta is the increase.
	if rx, tx := s.calculateDelta(vm, 1500, 2500); rx != 500 || tx != 500 {
		t.Fatalf("increase: want (500,500), got (%d,%d)", rx, tx)
	}

	// Counter reset (reboot): current < last → count the current value as fresh.
	if rx, tx := s.calculateDelta(vm, 100, 50); rx != 100 || tx != 50 {
		t.Fatalf("reset: want (100,50), got (%d,%d)", rx, tx)
	}

	// Continues normally from the post-reset baseline.
	if rx, tx := s.calculateDelta(vm, 300, 50); rx != 200 || tx != 0 {
		t.Fatalf("post-reset increase: want (200,0), got (%d,%d)", rx, tx)
	}

	// Independent VM tracks its own baseline.
	if rx, tx := s.calculateDelta("vm-2", 9999, 9999); rx != 0 || tx != 0 {
		t.Fatalf("new vm baseline: want (0,0), got (%d,%d)", rx, tx)
	}

	// ClearVMCounters resets the baseline (next report yields 0 again).
	s.ClearVMCounters(vm)
	if rx, tx := s.calculateDelta(vm, 500, 500); rx != 0 || tx != 0 {
		t.Fatalf("after clear: want (0,0), got (%d,%d)", rx, tx)
	}
}
