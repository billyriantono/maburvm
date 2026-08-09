package storage

import (
	"testing"
	"time"
)

// A flat deadline is what broke this: an hour is absurd for a 1 GB disk and
// fatal for a 90 GB one, and the fatal case only reveals itself after the export
// has already run for an hour.
func TestExportTimeoutScalesWithSize(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)

	tests := []struct {
		name    string
		size    int64
		atLeast time.Duration
		why     string
	}{
		{
			name:    "empty or unknown source still gets the base budget",
			size:    0,
			atLeast: compressedExportBaseTimeout,
		},
		{
			name:    "a 12 GB disk gets well over an hour",
			size:    12 * gib,
			atLeast: 90 * time.Minute,
			why:     "measured at 2 MiB/s with two exports competing, 12 GB takes ~100 minutes",
		},
		{
			name:    "a 39 GB disk gets several hours",
			size:    39 * gib,
			atLeast: 5 * time.Hour,
			why:     "this size failed in production against the old flat one-hour ceiling",
		},
		{
			name:    "a 91 GB disk is not cut off",
			size:    91 * gib,
			atLeast: 12 * time.Hour,
			why:     "the largest disk on the fleet must be backupable at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exportTimeout(tt.size)
			if got < tt.atLeast {
				t.Errorf("exportTimeout(%d) = %s, want at least %s. %s", tt.size, got, tt.atLeast, tt.why)
			}
		})
	}
}

// The budget must stay below the slowest rate actually observed, or the deadline
// becomes a second way to lose hours of completed work.
func TestExportBudgetIsBelowObservedThroughput(t *testing.T) {
	const slowestObserved = 2 * 1024 * 1024 // MiB/s, two exports competing for one disk
	if compressedExportBytesPerSecond > slowestObserved {
		t.Errorf("budget of %d B/s exceeds the slowest rate measured on a node (%d B/s)",
			compressedExportBytesPerSecond, slowestObserved)
	}
}

func TestExportTimeoutIsMonotonic(t *testing.T) {
	prev := exportTimeout(0)
	for _, gb := range []int64{1, 10, 40, 91, 512} {
		got := exportTimeout(gb * 1024 * 1024 * 1024)
		if got <= prev {
			t.Errorf("a %d GB source got %s, which is not more than the previous %s", gb, got, prev)
		}
		prev = got
	}
}
