//go:build pg

package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// 074 runs on a database built from every earlier migration, again on top of
// itself, down, and up again; existing rows keep no limits.
func TestVMLimitsMigrationIsRerunnable(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn), "a second boot applies nothing and fails on nothing")
	up, down := migrationSQL(t, "074_vm_limits_vcpu_ratio.up.sql"), migrationSQL(t, "074_vm_limits_vcpu_ratio.down.sql")
	for _, step := range []struct{ name, sql string }{{"074 run twice", up}, {"074 down", down}, {"074 up after down", up}} {
		_, err := raw.Exec(step.sql)
		require.NoError(t, err, step.name)
	}
	var cols []string
	require.NoError(t, db.Raw(`SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE (table_name = 'vms' AND column_name IN ('limits','throttle','limits_note'))
		   OR (table_name = 'plans' AND column_name = 'limits')
		   OR (table_name = 'nodes' AND column_name IN ('cpu_count','vcpu_ratio'))`).Scan(&cols).Error)
	assert.Len(t, cols, 6, "%v", cols)
}

// The JSON columns and the node lock, on the database they run on.
func TestVMLimitsOnPostgres(t *testing.T) {
	db, _, _ := pgMigratedDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const (
		user, node, tmpl = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
		vm, orphan       = "55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666"
		gone             = "77777777-7777-7777-7777-777777777777"
	)
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'owner@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token, cpu_count) VALUES (?, 'n1', '192.0.2.1', 't', 4)`, node)
	exec(`INSERT INTO nodes (id, name, ip_address, token, deleted_at) VALUES (?, 'n2', '192.0.2.2', 't2', NOW())`, gone)
	exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES (?, 'debian', '12', '/x', 'c')`, tmpl)
	// A VM on a node the panel no longer has: limits are stored, and the note says the node was not reached.
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES
		(?, ?, ?, 'web', ?, '{"cpu":3,"ram":2048,"disk":20}', 'running'),
		(?, ?, ?, 'old', ?, '{"cpu":1,"ram":1024,"disk":20}', 'stopped')`, vm, user, gone, tmpl, orphan, user, node, tmpl)

	var limits string
	require.NoError(t, db.Raw(`SELECT limits::text FROM vms WHERE id = ?`, orphan).Scan(&limits).Error)
	assert.Equal(t, "{}", limits, "existing VMs have no limits")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, logger)
	res, err := svc.SetVMLimits(ctx, vm, models.VMLimits{CPUCapPercent: 40, ReadIOPS: 900, WriteMBps: 50})
	require.NoError(t, err)
	assert.False(t, res.Applied)
	res, err = svc.ThrottleVM(ctx, vm, models.VMLimits{CPUCapPercent: 10, IOPS: 100})
	require.NoError(t, err)

	got, err := svc.GetVMRecord(ctx, vm)
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 40, ReadIOPS: 900, WriteMBps: 50}, got.Limits)
	require.NotNil(t, got.Throttle)
	assert.Equal(t, 10, got.Throttle.CPUCapPercent)
	require.NotNil(t, got.LimitsNote)
	_, err = svc.UnthrottleVM(ctx, vm)
	require.NoError(t, err)
	got, _ = svc.GetVMRecord(ctx, vm)
	assert.Nil(t, got.Throttle)

	// A plan's limits through the model.
	plans := service.NewPlanService(repository.NewPlanRepository(db))
	plan, err := plans.CreatePlan(ctx, &service.PlanRequest{Name: "small", CPU: 1, RAM: 1024, Disk: 20, Limits: &models.VMLimits{IOPS: 500}})
	require.NoError(t, err)
	reread, err := plans.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	assert.Equal(t, 500, reread.Limits.IOPS)

	// vCPU admission locks the node row inside the transaction that records the VM.
	exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', '{"vcpuAllocationRatio": 1}')`)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return service.AdmitNodeVCPU(ctx, tx, node, 3)
	}), "1 of 4 vCPUs used, 3 more fit")
	err = db.Transaction(func(tx *gorm.DB) error { return service.AdmitNodeVCPU(ctx, tx, node, 4) })
	assert.ErrorIs(t, err, service.ErrNodeCapacityExceeded)

	exec(`UPDATE nodes SET vcpu_ratio = 2 WHERE id = ?`, node)
	var n models.Node
	require.NoError(t, db.Where("id = ?", node).First(&n).Error)
	alloc, err := service.NodeAllocationOf(ctx, db, n)
	require.NoError(t, err)
	assert.Equal(t, 1, alloc.VCPUAllocated)
	assert.Equal(t, 8, alloc.VCPULimit)
}
