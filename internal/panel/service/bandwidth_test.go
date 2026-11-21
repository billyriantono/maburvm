package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
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

// A card taken off a running VM takes its bytes out of the VM's summed
// counters. That drop is a new baseline, not a restart that would count the
// rest of the VM's traffic since boot again.
func TestCardRemovalIsNotTraffic(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE vm_traffic_counters (vm_id TEXT PRIMARY KEY, rx_bytes BIGINT NOT NULL DEFAULT 0,
		tx_bytes BIGINT NOT NULL DEFAULT 0, updated_at DATETIME)`).Error)
	ctx := context.Background()
	repo := repository.NewBandwidthUsageRepository(db)
	s := NewBandwidthService(repo, slog.Default())
	report := func(rx, tx int64) [2]int64 {
		r := VMBandwidthReport{VMID: "vm-1", RxBytes: rx, TxBytes: tx}
		drx, dtx := s.reportDelta(ctx, r)
		require.NoError(t, repo.SaveCounters(ctx, r.VMID, rx, tx))
		return [2]int64{drx, dtx}
	}
	assert.Equal(t, [2]int64{0, 0}, report(1000, 1000))
	assert.Equal(t, [2]int64{4000, 2000}, report(5000, 3000))
	assert.Equal(t, [2]int64{100, 50}, report(100, 50), "a restart: the reading is traffic")
	assert.Equal(t, [2]int64{3900, 1950}, report(4000, 2000))
	require.NoError(t, repo.ForgetCounters(ctx, "vm-1"))
	assert.Equal(t, [2]int64{0, 0}, report(3000, 1500), "a card left: a new baseline")
	assert.Equal(t, [2]int64{500, 100}, report(3500, 1600))
}
