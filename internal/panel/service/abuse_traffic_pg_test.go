//go:build pg

package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// Transfer on Postgres: the plan's reset day and counted direction, the
// account's override, counters kept across a panel restart, and a pool.
func TestTransferAccountingOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const (
		user, node, tmpl, plan = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333",
			"44444444-4444-4444-4444-444444444444", "55555555-5555-5555-5555-555555555555"
	)
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'u@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', '/i.qcow2')`, tmpl)
	exec(`INSERT INTO plans (id, name, cpu, ram, disk, transfer_reset_day, transfer_counts) VALUES (?, 'p', 1, 1024, 10, 15, 'out')`, plan)
	var vmA, vmB string
	require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources, plan_id) VALUES (?, ?, 'a', ?, '{}', ?) RETURNING id`,
		user, node, tmpl, plan).Scan(&vmA).Error)
	require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES (?, ?, 'b', ?, '{}') RETURNING id`,
		user, node, tmpl).Scan(&vmB).Error)
	exec(`INSERT INTO networks (vm_id, ip_address, bandwidth_quota_gb) VALUES (?, '203.0.113.10', 1), (?, '203.0.113.11', 1)`, vmA, vmB)

	repo := repository.NewBandwidthUsageRepository(db)
	day, counts := repo.TransferTerms(ctx, vmA)
	require.Equal(t, 15, day)
	require.Equal(t, models.TransferCountOut, counts)
	day, counts = repo.TransferTerms(ctx, vmB)
	require.Equal(t, 1, day, "a VM without a plan keeps the calendar month")
	require.Equal(t, models.TransferCountBoth, counts)

	// Only what the VM sent counts on an "out" plan; both are still recorded.
	svc := NewBandwidthService(repo, slog.Default())
	_, err := svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmA, RxBytes: 1000, TxBytes: 100}})
	require.NoError(t, err)
	_, err = svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmA, RxBytes: 6000, TxBytes: 600}})
	require.NoError(t, err)
	u, err := svc.GetVMUsage(ctx, vmA, node)
	require.NoError(t, err)
	require.Equal(t, int64(500), u.TotalBytes)
	require.Equal(t, int64(5000), u.RxBytes)
	start, _ := models.TransferPeriod(time.Now(), 15)
	require.True(t, u.PeriodStart.Equal(start), "period starts on the 15th: %s", u.PeriodStart)

	// A restarted panel differences against the kept counters instead of
	// starting over and losing the traffic in between.
	restarted := NewBandwidthService(repo, slog.Default())
	_, err = restarted.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmA, RxBytes: 6000, TxBytes: 900}})
	require.NoError(t, err)
	u, err = restarted.GetVMUsage(ctx, vmA, node)
	require.NoError(t, err)
	require.Equal(t, int64(800), u.TotalBytes)

	// The account's reset day wins over the plan's.
	require.NoError(t, SaveAccountTransfer(ctx, db, user, AccountTransfer{ResetDay: intPtr(3)}))
	day, _ = repo.TransferTerms(ctx, vmA)
	require.Equal(t, 3, day)
	require.Error(t, SaveAccountTransfer(ctx, db, user, AccountTransfer{ResetDay: intPtr(29)}))
	require.NoError(t, SaveAccountTransfer(ctx, db, user, AccountTransfer{}))

	// Pooled: the two 1 GB allowances make a 2 GB pool, and going over it
	// flags both VMs at once.
	require.NoError(t, SaveAccountTransfer(ctx, db, user, AccountTransfer{Pooled: true}))
	const gb = int64(1) << 30
	_, err = svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmB, RxBytes: 1, TxBytes: 1}})
	require.NoError(t, err)
	_, err = svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmB, RxBytes: 1 + gb, TxBytes: 1}})
	require.NoError(t, err)
	// vmA's period before its reset day changed also contains today; it no
	// longer counts and must not be added to the pool.
	oldStart, oldEnd := models.TransferPeriod(time.Now(), 1)
	exec(`INSERT INTO bandwidth_usages (vm_id, node_id, period_start, period_end, total_bytes, quota_bytes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NOW() - INTERVAL '1 day')`, vmA, node, oldStart, oldEnd, 5*gb, gb)
	pool := repo.AccountPool(ctx, vmB)
	require.True(t, pool.Pooled)
	require.Equal(t, 2*gb, pool.QuotaBytes)
	ub, err := svc.GetVMUsage(ctx, vmB, node)
	require.NoError(t, err)
	require.False(t, ub.Exceeded, "1 GB used of a 2 GB pool is not over, though it is this VM's whole allowance")

	exceeded, err := svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmB, RxBytes: 1 + 2*gb, TxBytes: 1}})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{vmA, vmB}, exceeded)
	exceeded, err = svc.ProcessHeartbeatBandwidth(ctx, node, []VMBandwidthReport{{VMID: vmB, RxBytes: 2 + 2*gb, TxBytes: 1}})
	require.NoError(t, err)
	require.Empty(t, exceeded, "a pool is flagged once")
}

func intPtr(v int) *int { return &v }

// Daily averages are weighted by the hours' samples and read back for long
// windows.
func TestDailyMetricsRollupOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	const vm = "66666666-6666-6666-6666-666666666666"
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -200)
	require.NoError(t, db.Exec(`INSERT INTO vm_metrics_hourly (vm_id, bucket, samples, cpu_usage, network_rx_bytes_per_sec) VALUES
		(?, ?, 60, 10, 1000), (?, ?, 20, 50, 3000), (?, ?, 60, 99, 0)`,
		vm, day.Add(time.Hour), vm, day.Add(5*time.Hour), vm, day.Add(24*time.Hour)).Error)

	repo := repository.NewMetricsRepository(db)
	require.NoError(t, repo.RollupVMDays(ctx, day, day.Add(24*time.Hour)))
	require.NoError(t, repo.RollupVMDays(ctx, day, day.Add(24*time.Hour)), "rolling a day up again replaces it")
	rows, err := repo.ListVMDays(ctx, vm, day.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1, "the next day is not over the range")
	require.InDelta(t, 20.0, rows[0].CPUUsage, 0.001) // (10*60 + 50*20) / 80
	require.Equal(t, int64(1500), rows[0].NetworkRxBytesPerSec)
	require.Equal(t, day.Add(24*time.Hour-time.Second), rows[0].RecordedAt.UTC())

	got, err := NewMetricsService(db).VMHistory(ctx, vm, 365*24*time.Hour, 500)
	require.NoError(t, err)
	require.Len(t, got, 1, "a year reads the daily averages")

	removed, err := repo.PruneVMDaysBefore(ctx, day.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
}

// An episode on Postgres: opened by the first reading over, acted on after
// the sustain readings (and recorded as failed when the action cannot be
// taken), kept with its readings, and ended once the guest is quiet.
func TestAbuseEpisodeOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	const node = "33333333-3333-3333-3333-333333333333"
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node).Error)
	s := NewAbuseService(db, nil, slog.Default())
	policy := DefaultAbusePolicy()
	policy.OutboundPPS.Action = models.AbuseActionRateLimit
	g := &models.GuestConnection{NodeID: node, MAC: "52:54:00:aa:bb:cc"}
	at := time.Now()
	reading := func(pps float64) {
		at = at.Add(time.Minute)
		s.evaluate(ctx, g, guestReadings{models.AbuseOutboundPPS: pps}, models.AbuseSample{At: at, PPSOut: pps}, policy)
	}

	reading(500_000)
	events, total, err := s.ListEvents(ctx, true, 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, models.AbuseStateNone, events[0].ActionState)
	require.Equal(t, "n1", events[0].NodeName)

	reading(600_000)
	events, _, err = s.ListEvents(ctx, true, 10, 0)
	require.NoError(t, err)
	require.Equal(t, models.AbuseActionRateLimit, events[0].Action)
	require.Equal(t, models.AbuseStateFailed, events[0].ActionState, "an unmanaged guest cannot be slowed")
	require.Equal(t, 2, events[0].Samples)
	require.Equal(t, 600_000.0, events[0].Peak)
	require.Len(t, events[0].Evidence, 2)

	reading(10)
	open, _, err := s.ListEvents(ctx, true, 10, 0)
	require.NoError(t, err)
	require.Empty(t, open, "back under the threshold ends it")
	all, _, err := s.ListEvents(ctx, false, 10, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NotNil(t, all[0].EndedAt)

	// A null-route set after the node was asked is not in its answer yet and
	// stays in force; one set before and missing from it has been lifted.
	fetched := time.Now()
	nullRoute := func(mac string, actedAt time.Time) string {
		ev := &models.AbuseEvent{ID: uuid.NewString(), NodeID: node, MAC: mac, Kind: models.AbuseInboundPPS, Action: models.AbuseActionNullRoute,
			ActionState: models.AbuseStateActive, Targets: "203.0.113.5", StartedAt: actedAt, LastSeenAt: actedAt, Evidence: []models.AbuseSample{}}
		require.NoError(t, db.Create(ev).Error)
		return ev.ID
	}
	fresh, lifted := nullRoute("52:54:00:00:00:01", fetched.Add(time.Second)), nullRoute("52:54:00:00:00:02", fetched.Add(-time.Minute))
	s.reconcileNullRoutes(ctx, node, nil, fetched)
	state := func(id string) (string, bool) {
		var ev models.AbuseEvent
		require.NoError(t, db.Where("id = ?", id).First(&ev).Error)
		return ev.ActionState, ev.EndedAt != nil
	}
	st, ended := state(fresh)
	require.Equal(t, models.AbuseStateActive, st)
	require.False(t, ended)
	st, ended = state(lifted)
	require.Equal(t, models.AbuseStateExpired, st)
	require.True(t, ended)

	// A guest the node no longer runs has its watching episode ended; one with
	// an action in force stays open for Release.
	reading(500_000)
	s.endGoneEpisodes(ctx, node, []string{"52:54:00:00:00:99"}, time.Now())
	open, _, err = s.ListEvents(ctx, true, 10, 0)
	require.NoError(t, err)
	require.Len(t, open, 1)
	require.Equal(t, fresh, open[0].ID)
}
