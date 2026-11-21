//go:build pg

package service

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// On Postgres: dedicated cores are never handed out twice, stay in the pool
// while held, and follow a resize; the pool's free count steers placement.
func TestDedicatedCoresOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const user, node, tmpl = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'u@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', '/i.qcow2')`, tmpl)
	vms := make([]string, 4)
	for i := range vms {
		require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES (?, ?, ?, ?, '{"cpu":2}') RETURNING id`,
			user, node, "vm"+string(rune('a'+i)), tmpl).Scan(&vms[i]).Error)
	}

	_, err := SetNodeCPUPool(ctx, db, node, "4-9")
	require.NoError(t, err)
	assert.Equal(t, 6, freeDedicatedCores(ctx, db, node))

	// Three orders at once for two cores each: the six are shared out exactly.
	var wg sync.WaitGroup
	got := make([]string, 3)
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = db.Transaction(func(tx *gorm.DB) error {
				cpus, err := allocateDedicatedTx(ctx, tx, node, vms[i], 2)
				if err != nil {
					return err
				}
				got[i] = cpus
				return writeVMHardware(tx, vms[i], models.Hardware{DedicatedCPU: true, PinnedCPUs: cpus})
			})
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for i := range got {
		require.NoError(t, errs[i])
		cpus, err := models.ParseCPUSet(got[i])
		require.NoError(t, err)
		require.Len(t, cpus, 2)
		for _, c := range cpus {
			require.False(t, seen[c], "core %d handed out twice", c)
			seen[c] = true
		}
	}
	assert.Zero(t, freeDedicatedCores(ctx, db, node))
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := allocateDedicatedTx(ctx, tx, node, vms[3], 1)
		return err
	})
	assert.ErrorIs(t, err, ErrNoDedicatedCPUs)

	pool, err := GetNodeCPUPool(ctx, db, node)
	require.NoError(t, err)
	assert.Equal(t, 6, pool.Size)
	assert.Equal(t, 6, pool.Used)
	assert.Len(t, pool.VMs, 3)
	_, err = SetNodeCPUPool(ctx, db, node, "4-8")
	assert.ErrorIs(t, err, ErrInvalidHardware, "a held core stays in the pool")
	_, err = SetNodeCPUPool(ctx, db, node, "4-11")
	require.NoError(t, err)

	// A resize keeps the cores it has and adds free ones; a shrink keeps the first.
	var grown string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		grown, err = resizeDedicatedTx(ctx, tx, node, vms[0], got[0], 4)
		return err
	}))
	held, _ := models.ParseCPUSet(got[0])
	all, _ := models.ParseCPUSet(grown)
	require.Len(t, all, 4)
	assert.Subset(t, all, held)
	assert.Subset(t, all, []int{10, 11})
	var shrunk string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		shrunk, err = resizeDedicatedTx(ctx, tx, node, vms[0], grown, 1)
		return err
	}))
	assert.Equal(t, models.FormatCPUSet(all[:1]), shrunk)
}

// A NAT VM's ports land on the destination: the same ones when free there,
// else the lowest free block, its forwards shifted with them.
func TestNATBlockMovesOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const user, src, dst, tmpl = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333",
		"55555555-5555-5555-5555-555555555555", "44444444-4444-4444-4444-444444444444"
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'u@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token, nat_port_start, nat_port_end, nat_address) VALUES
		(?, 'alpha', '192.0.2.1', 'ta', 30000, 30199, '203.0.113.10'), (?, 'beta', '192.0.2.2', 'tb', 30000, 30199, '203.0.113.20')`, src, dst)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', '/i.qcow2')`, tmpl)
	newVM := func(host, node string) string {
		var id string
		require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES (?, ?, ?, ?, '{}') RETURNING id`,
			user, node, host, tmpl).Scan(&id).Error)
		return id
	}
	vm, other := newVM("web", src), newVM("other", dst)
	var netID string
	require.NoError(t, db.Raw(`INSERT INTO networks (vm_id, ip_address) VALUES (?, '10.20.0.5') RETURNING id`, vm).Scan(&netID).Error)
	exec(`INSERT INTO nat_port_blocks (vm_id, node_id, start_port, port_count) VALUES (?, ?, 30000, 20), (?, ?, 30000, 20)`, vm, src, other, dst)
	exec(`INSERT INTO port_forwards (vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES
		(?, ?, ?, 30000, 22, 'tcp'), (?, ?, ?, 30005, 80, 'tcp')`, vm, netID, src, vm, netID, src)

	plan, err := planNATMove(ctx, db, vm, dst)
	require.NoError(t, err)
	assert.False(t, plan.SamePort)
	assert.Equal(t, 30020, plan.Start, "the lowest free block, past the other VM's")
	assert.Equal(t, "203.0.113.20", plan.Address)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := moveNATBlockTx(ctx, tx, vm, dst)
		return err
	}))
	var b models.NATPortBlock
	require.NoError(t, db.Where("vm_id = ?", vm).First(&b).Error)
	assert.Equal(t, dst, b.NodeID)
	assert.Equal(t, 30020, b.StartPort)
	var ports []int
	require.NoError(t, db.Raw(`SELECT external_port FROM port_forwards WHERE vm_id = ? AND node_id = ? ORDER BY external_port`, vm, dst).Scan(&ports).Error)
	assert.Equal(t, []int{30020, 30025}, ports)

	// Back to a node where its ports are free: it keeps them.
	plan, err = planNATMove(ctx, db, vm, src)
	require.NoError(t, err)
	assert.True(t, plan.SamePort)
	assert.Equal(t, 30020, plan.Start)

	// A node with no NAT range cannot take it.
	exec(`UPDATE nodes SET nat_port_start = 0, nat_port_end = 0 WHERE id = ?`, src)
	_, err = planNATMove(ctx, db, vm, src)
	assert.ErrorIs(t, err, ErrNATPortsExhausted)
}

// A schedule's keep is held to the plan's snapshot allowance, its snapshots are
// handed to the VM when it is removed, and a run that has no room says why.
func TestSnapshotSchedulesOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const user, node, tmpl, plan = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333",
		"44444444-4444-4444-4444-444444444444", "66666666-6666-6666-6666-666666666666"
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'u@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', '/i.qcow2')`, tmpl)
	exec(`INSERT INTO plans (id, name, cpu, ram, disk, max_snapshots) VALUES (?, 'small', 1, 1024, 20, 3)`, plan)
	var vm string
	require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources, plan_id, status) VALUES (?, ?, 'web', ?, '{}', ?, 'stopped') RETURNING id`,
		user, node, tmpl, plan).Scan(&vm).Error)
	exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'by-hand', '', 'completed')`, vm)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewSnapshotService(db, repository.NewSnapshotRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, log)

	_, err := svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 24, Keep: 3})
	var allowance *AllowanceError
	require.ErrorAs(t, err, &allowance)
	assert.Contains(t, allowance.Message, "can keep 2")
	_, err = svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 0, Keep: 1})
	assert.ErrorIs(t, err, ErrInvalidSchedule)
	_, err = svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 24, Keep: 1, DiskOnly: true})
	assert.ErrorIs(t, err, ErrSnapshotOptionsUnsupported, "a node that cannot take disk-only snapshots refuses them")

	sch, err := svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 24, Keep: 2})
	require.NoError(t, err)
	assert.True(t, sch.Enabled)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), sch.NextRunAt, time.Minute)
	off := false
	sch, err = svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 24, Keep: 2, Enabled: &off})
	require.NoError(t, err)
	assert.False(t, sch.Enabled, "a schedule can be paused")
	on := true
	_, err = svc.SetSnapshotSchedule(ctx, vm, SnapshotScheduleRequest{IntervalHours: 24, Keep: 2, Enabled: &on})
	require.NoError(t, err)

	// The allowance is full of snapshots taken by hand: the run records why
	// and waits a whole interval.
	exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'b2', '', 'completed'), (?, 'b3', '', 'completed')`, vm, vm)
	exec(`UPDATE snapshot_schedules SET next_run_at = NOW() - interval '1 minute'`)
	now := time.Now()
	svc.runDueSchedules(ctx, now)
	require.NoError(t, db.Where("vm_id = ?", vm).First(sch).Error)
	assert.True(t, strings.Contains(sch.LastError, "allowance"), sch.LastError)
	assert.WithinDuration(t, now.Add(24*time.Hour), sch.NextRunAt, time.Minute)

	// A run that cannot take its snapshot removes none of the schedule's: one
	// by hand and two scheduled fill the allowance, so a run would prune one.
	exec(`DELETE FROM snapshots WHERE name IN ('b2', 'b3')`)
	exec(`INSERT INTO snapshots (vm_id, name, disk_path, status, schedule_id, created_at) VALUES
		(?, 'auto-1', '', 'completed', ?, NOW() - interval '2 days'), (?, 'auto-2', '', 'completed', ?, NOW() - interval '1 day')`, vm, sch.ID, vm, sch.ID)
	scheduledLeft := func() int64 {
		t.Helper()
		var n int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM snapshots WHERE schedule_id IS NOT NULL`).Scan(&n).Error)
		return n
	}
	exec(`UPDATE vms SET rescue_mode = true, rescue_kind = 'image' WHERE id = ?`, vm)
	exec(`UPDATE snapshot_schedules SET next_run_at = NOW() - interval '1 minute'`)
	svc.runDueSchedules(ctx, time.Now())
	assert.EqualValues(t, 2, scheduledLeft(), "a VM in rescue mode lost a scheduled snapshot")
	require.NoError(t, db.Where("vm_id = ?", vm).First(sch).Error)
	assert.Contains(t, sch.LastError, "rescue")

	// A snapshot still being taken holds the run back, removing nothing.
	exec(`UPDATE vms SET rescue_mode = false, rescue_kind = '' WHERE id = ?`, vm)
	exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'by-hand-2', '', 'pending')`, vm)
	exec(`UPDATE snapshot_schedules SET next_run_at = NOW() - interval '1 minute'`)
	svc.runDueSchedules(ctx, time.Now())
	assert.EqualValues(t, 2, scheduledLeft(), "a pending snapshot made the schedule prune")
	require.NoError(t, db.Where("vm_id = ?", vm).First(sch).Error)
	assert.True(t, sch.NextRunAt.Before(time.Now()), "a run held back by a pending snapshot is tried again")

	// Pruning that cannot make room prunes nothing: with a second snapshot by
	// hand in a three-snapshot allowance, the two scheduled ones would go and
	// the run would still be refused.
	exec(`UPDATE snapshots SET status = 'completed' WHERE name = 'by-hand-2'`)
	exec(`UPDATE plans SET max_snapshots = 2 WHERE id = ?`, plan)
	svc.runDueSchedules(ctx, time.Now())
	assert.EqualValues(t, 2, scheduledLeft(), "the schedule pruned snapshots it could not replace")
	require.NoError(t, db.Where("vm_id = ?", vm).First(sch).Error)
	assert.Contains(t, sch.LastError, "allowance")
	exec(`DELETE FROM snapshots WHERE name = 'by-hand-2'`)
	exec(`UPDATE plans SET max_snapshots = 3 WHERE id = ?`, plan)

	// A VM being deleted is skipped with a reason, not snapshotted.
	exec(`UPDATE vms SET status = 'deleting' WHERE id = ?`, vm)
	exec(`UPDATE snapshot_schedules SET next_run_at = NOW() - interval '1 minute'`)
	svc.runDueSchedules(ctx, time.Now())
	require.NoError(t, db.Where("vm_id = ?", vm).First(sch).Error)
	assert.Contains(t, sch.LastError, "deleting")

	// Removing the schedule hands its snapshots to the VM.
	require.NoError(t, svc.DeleteSnapshotSchedule(ctx, vm))
	var scheduled int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM snapshots WHERE schedule_id IS NOT NULL`).Scan(&scheduled).Error)
	assert.Zero(t, scheduled)
	_, err = svc.GetSnapshotSchedule(ctx, vm)
	assert.ErrorIs(t, err, ErrSnapshotScheduleNotFound)
}
