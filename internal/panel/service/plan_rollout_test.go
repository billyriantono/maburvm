package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intp(v int) *int { return &v }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ADM-15: what applying a plan changes on one VM, in words, and what waits.
func TestDiffPlan(t *testing.T) {
	plan := &models.Plan{CPU: 2, RAM: 4096, Disk: 40, BandwidthMbps: 200, DataQuotaGB: 500,
		OverQuotaPolicy: models.OverQuotaSuspend, SMTPPolicy: models.SMTPBlocked, Limits: models.VMLimits{CPUCapPercent: 50}}
	running := &models.VM{ID: "a", Hostname: "web", Status: models.VMStatusRunning, Resources: models.Resources{CPU: 1, RAM: 2048, Disk: 20}}
	nic := &models.Network{BandwidthLimit: 100, OverQuotaPolicy: models.OverQuotaThrottle}

	d := diffPlan(plan, running, nic)
	assert.Equal(t, []string{"vCPU 1 to 2", "Memory 2048 MB to 4096 MB", "Disk 20 GB to 40 GB", "CPU and disk limits as on the plan",
		"Speed 100 Mbps to 200 Mbps", "Monthly transfer unlimited to 500 GB", "Over-quota policy as on the plan (suspend)",
		"Block outbound mail on port 25"}, d.Changes)
	assert.True(t, d.NeedsStop, "a running VM keeps its size until it is stopped")
	assert.Equal(t, &models.Resources{CPU: 2, RAM: 4096, Disk: 40}, d.resize)
	assert.True(t, d.limits && d.network && d.blockMail)

	stopped := &models.VM{Status: models.VMStatusStopped, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 80},
		Limits: plan.Limits, SMTPBlocked: true}
	d = diffPlan(&models.Plan{CPU: 2, RAM: 4096, Disk: 40, Limits: plan.Limits}, stopped, nil)
	assert.Empty(t, d.Changes, "a disk bigger than the plan's stays, and nothing else differs")
	assert.Nil(t, d.resize)
	assert.Contains(t, d.Notes, "Disk stays at 80 GB: a disk only grows.")
	assert.Contains(t, d.Notes, "Outbound mail stays blocked on this VM. Unblock it on the VM's page if that is intended.")

	note := "node-a did not answer"
	stopped.LimitsNote = &note
	d = diffPlan(&models.Plan{CPU: 4, RAM: 4096, Disk: 80, Limits: plan.Limits}, stopped, nil)
	assert.Equal(t, []string{"vCPU 2 to 4", "CPU and disk limits sent to the node again"}, d.Changes)
	assert.False(t, d.NeedsStop, "a stopped VM is resized now")

	d = diffPlan(plan, &models.VM{Status: models.VMStatusCreating}, nil)
	assert.Empty(t, d.Changes)
	assert.Equal(t, []string{"Being created: apply again once it is ready."}, d.Notes)
}

func TestAllowanceCheck(t *testing.T) {
	assert.NoError(t, allowanceCheck(nil, 99, "snapshot", "snapshots", ""))
	assert.NoError(t, allowanceCheck(intp(2), 1, "snapshot", "snapshots", ""))
	assert.EqualError(t, allowanceCheck(intp(2), 2, "snapshot", "snapshots", "Delete one to take another."),
		"This VM's plan includes 2 snapshots and it has 2. Delete one to take another.")
	assert.EqualError(t, allowanceCheck(intp(1), 3, "backup", "backups", "Delete one."),
		"This VM's plan includes 1 backup and it has 3. Delete one.")
	err := allowanceCheck(intp(0), 0, "IPv6", "IPv6", "")
	var ae *AllowanceError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, "This VM's plan does not include IPv6.", ae.Message)
}

// ADM-15: applying a plan reaches the VMs on it and no others, applies what
// can change live, and leaves a running VM's size alone.
func TestRolloutPlan(t *testing.T) {
	ctx := context.Background()
	s, db, node := limitsService(t)
	for _, stmt := range []string{
		`ALTER TABLE vms ADD COLUMN plan_id TEXT`,
		`ALTER TABLE vms ADD COLUMN smtp_blocked BOOLEAN NOT NULL DEFAULT 0`,
		`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, ip_address TEXT NOT NULL, bandwidth_limit INTEGER DEFAULT 0, bandwidth_quota_gb INTEGER DEFAULT 0, over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0, throttled BOOLEAN DEFAULT 0, vlan_id INTEGER, anti_spoofing BOOLEAN DEFAULT 1, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE bandwidth_usages (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, node_id TEXT NOT NULL, period_start DATETIME NOT NULL, period_end DATETIME NOT NULL, total_bytes INTEGER DEFAULT 0, quota_bytes INTEGER DEFAULT 0, exceeded BOOLEAN DEFAULT 0, blocked_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	plan := seedPlan(t, db, true) // 2 vCPU, 2048 MB, 40 GB, 100 Mbps, 500 GB
	require.NoError(t, db.Model(plan).Update("limits", `{"cpu_cap_percent":50}`).Error)
	for _, stmt := range []string{
		`UPDATE vms SET plan_id = '` + plan.ID + `', resources = '{"cpu":2,"ram":2048,"disk":40}' WHERE id = 'vm-1'`,
		`INSERT INTO vms (id, node_id, hostname, resources, status, plan_id) VALUES ('vm-2', 'n1', 'db', '{"cpu":1,"ram":2048,"disk":40}', 'running', '` + plan.ID + `')`,
		`INSERT INTO vms (id, node_id, hostname, resources, status, plan_id) VALUES ('vm-3', 'n1', 'gone', '{"cpu":1,"ram":2048,"disk":40}', 'deleting', '` + plan.ID + `')`,
		`INSERT INTO vms (id, node_id, hostname, resources, status) VALUES ('vm-4', 'n1', 'other', '{"cpu":1,"ram":2048,"disk":40}', 'running')`,
		`INSERT INTO networks (id, vm_id, ip_address, bandwidth_limit) VALUES ('net-2', 'vm-2', '203.0.113.2', 100)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	s.planRepo = repository.NewPlanRepository(db)
	s.networkRepo = repository.NewNetworkRepository(db)
	// No queue: the speed and quota are stored, but the node cannot be told.
	net := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)

	preview, err := s.RolloutPlan(ctx, plan.ID, net, false)
	require.NoError(t, err)
	require.Len(t, preview.VMs, 2, "only the plan's VMs that are not being deleted")
	byHost := map[string]PlanRolloutVM{}
	for _, v := range preview.VMs {
		byHost[v.Hostname] = v
	}
	assert.Equal(t, []string{"vCPU 1 to 2", "CPU and disk limits as on the plan", "Monthly transfer unlimited to 500 GB",
		"Over-quota policy as on the plan (suspend)"}, byHost["db"].Changes)
	assert.True(t, byHost["db"].NeedsStop)
	assert.Equal(t, []string{"CPU and disk limits as on the plan"}, byHost["web"].Changes)
	assert.Empty(t, node.sent, "a preview changes nothing")

	applied, err := s.RolloutPlan(ctx, plan.ID, net, true)
	require.NoError(t, err)
	for _, v := range applied.VMs {
		byHost[v.Hostname] = v
	}
	assert.Equal(t, RolloutApplied, byHost["web"].Outcome)
	assert.Equal(t, RolloutPartial, byHost["db"].Outcome, "the limits went through; the speed could not be sent")
	require.Len(t, byHost["db"].Errors, 1)
	assert.Contains(t, byHost["db"].Errors[0], "Speed and transfer quota: the plan's limits are saved")
	assert.Len(t, node.sent, 2)

	vm2, err := s.GetVMRecord(ctx, "vm-2")
	require.NoError(t, err)
	assert.Equal(t, 1, vm2.Resources.CPU, "a running VM is never stopped or resized for a plan")
	assert.Equal(t, 50, vm2.Limits.CPUCapPercent)
	var quota int64
	require.NoError(t, db.Table("networks").Where("id = 'net-2'").Pluck("bandwidth_quota_gb", &quota).Error)
	assert.EqualValues(t, 500, quota)
	vm4, err := s.GetVMRecord(ctx, "vm-4")
	require.NoError(t, err)
	assert.Zero(t, vm4.Limits.CPUCapPercent, "a VM on no plan is not touched")

	again, err := s.RolloutPlan(ctx, plan.ID, net, true)
	require.NoError(t, err)
	for _, v := range again.VMs {
		if v.Hostname == "web" {
			assert.Equal(t, RolloutUpToDate, v.Outcome)
		}
	}
	_, err = s.RolloutPlan(ctx, "missing", net, false)
	assert.ErrorIs(t, err, ErrPlanNotFound)
}

// ADM-15: the plan's allowances refuse one more snapshot, backup or address,
// with a message that says why; a VM on no plan keeps the old limits only.
func TestPlanAllowancesAreEnforced(t *testing.T) {
	ctx := context.Background()
	db := setupVMIPAMTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE snapshots (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, name TEXT NOT NULL, disk_path TEXT NOT NULL DEFAULT '', status TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '')`,
		`CREATE TABLE backup_schedules (id TEXT PRIMARY KEY, vm_id TEXT, schedule TEXT, status TEXT, storage_provider TEXT, compression TEXT, retention_policy TEXT, next_run_at DATETIME, last_run_at DATETIME, last_backup_id TEXT, full_every_days INTEGER DEFAULT 0, chain_reset_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	plan := seedPlan(t, db, true)
	require.NoError(t, db.Model(plan).Updates(map[string]any{"max_snapshots": 1, "max_backups": 0, "max_extra_ipv4": 0}).Error)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "capped", OSTemplateID: tmpl.ID, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)
	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("plan_id", plan.ID).Error)
	require.NoError(t, db.Create(&models.Snapshot{VMID: vm.ID, Name: "first", Status: models.SnapshotStatusComplete}).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vm.ID, IPAddress: "203.0.113.30"}).Error)

	snaps := NewSnapshotService(db, repository.NewSnapshotRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, discardLogger())
	_, err := snaps.CreateSnapshot(ctx, &CreateSnapshotRequest{VMID: vm.ID, Name: "second", UserID: user.ID.String()})
	var ae *AllowanceError
	require.True(t, errors.As(err, &ae), "%v", err)
	assert.Equal(t, "This VM's plan includes 1 snapshot and it has 1. Delete one to take another.", ae.Message)

	backups := NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, nil, discardLogger())
	_, err = backups.CreateBackup(ctx, &CreateBackupRequest{VMID: vm.ID})
	require.True(t, errors.As(err, &ae), "%v", err)
	assert.Equal(t, "This VM's plan does not include backups.", ae.Message)
	_, err = backups.ConfigureSchedule(ctx, &ConfigureScheduleRequest{VMID: vm.ID, Schedule: "0 2 * * *"})
	require.True(t, errors.As(err, &ae), "a schedule has nothing to keep")

	net := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	net.SetIPAMService(NewIPAMService(db, repository.NewIPAMRepository(db)))
	_, err = net.AssignIPAddress(ctx, vm.ID, "", "")
	require.True(t, errors.As(err, &ae), "%v", err)
	assert.Equal(t, "This VM's plan does not include extra IPv4 addresses.", ae.Message)

	// Raised: the same requests get past the allowance (and stop later, at
	// the pools or the queue this test does not have).
	require.NoError(t, db.Model(plan).Updates(map[string]any{"max_extra_ipv4": 1}).Error)
	_, err = net.AssignIPAddress(ctx, vm.ID, "", "")
	assert.False(t, errors.As(err, &ae), "%v", err)

	// A deleted plan still governs the VMs sold on it.
	require.NoError(t, db.Delete(plan).Error)
	_, err = snaps.CreateSnapshot(ctx, &CreateSnapshotRequest{VMID: vm.ID, Name: "third", UserID: user.ID.String()})
	assert.True(t, errors.As(err, &ae), "%v", err)
}

// ADM-15: scheduled backups rotate within the plan's allowance; manual ones
// are never removed to make room.
func TestScheduledBackupsKeepToThePlan(t *testing.T) {
	ctx := context.Background()
	s, db := newSchedulerTestService(t)
	for _, stmt := range []string{
		`ALTER TABLE vms ADD COLUMN plan_id TEXT`,
		`CREATE TABLE backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '')`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	plan := seedPlan(t, db, true)
	require.NoError(t, db.Model(plan).Update("max_backups", 2).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, plan_id) VALUES ('vm-a', 'u', 'n', 'a', ?)`, plan.ID).Error)
	vm, err := repository.NewVMRepository(db).GetByID(ctx, "vm-a")
	require.NoError(t, err)
	add := func(id string, kind models.BackupType, age time.Duration) {
		require.NoError(t, db.Create(&models.Backup{ID: id, VMID: "vm-a", StorageProvider: models.BackupStoreNode, StoragePath: "/b/" + id,
			BackupType: kind, Status: models.BackupStatusCompleted, CreatedAt: time.Now().Add(-age)}).Error)
	}
	remaining := func() []string {
		var ids []string
		require.NoError(t, db.Model(&models.Backup{}).Order("id").Pluck("id", &ids).Error)
		return ids
	}

	add("m1", models.BackupTypeManual, 72*time.Hour)
	add("m2", models.BackupTypeManual, 48*time.Hour)
	ok, why, err := s.scheduledBackupAllowed(ctx, vm)
	require.NoError(t, err)
	assert.False(t, ok, "the allowance is all manual backups")
	assert.Contains(t, why, "manual")

	require.NoError(t, db.Model(plan).Update("max_backups", 4).Error)
	add("s0", models.BackupTypeSchedule, 36*time.Hour)
	add("s1", models.BackupTypeSchedule, 24*time.Hour)
	ok, _, err = s.scheduledBackupAllowed(ctx, vm)
	require.NoError(t, err)
	assert.True(t, ok, "at the allowance with a scheduled backup to replace")
	// The run that replaces it is still going: nothing is removed for it yet.
	require.NoError(t, db.Create(&models.Backup{ID: "s2", VMID: "vm-a", StorageProvider: models.BackupStoreNode, StoragePath: "/b/s2",
		BackupType: models.BackupTypeSchedule, Status: models.BackupStatusPending, CreatedAt: time.Now().Add(-time.Hour)}).Error)
	require.NoError(t, s.pruneOverAllowance(ctx, "vm-a"))
	assert.Equal(t, []string{"m1", "m2", "s0", "s1", "s2"}, remaining(), "a run that may still fail costs no backup")
	require.NoError(t, db.Model(&models.Backup{}).Where("id = 's2'").Update("status", models.BackupStatusCompleted).Error)
	require.NoError(t, s.pruneOverAllowance(ctx, "vm-a"))
	assert.Equal(t, []string{"m1", "m2", "s1", "s2"}, remaining(), "the oldest scheduled one made room; manual ones stay")

	// Lowered below the manual ones: the newest scheduled backup still stays.
	require.NoError(t, db.Model(plan).Update("max_backups", 1).Error)
	require.NoError(t, s.pruneOverAllowance(ctx, "vm-a"))
	assert.Equal(t, []string{"m1", "m2", "s2"}, remaining())

	// No backups on the plan: schedules stop, and nothing is removed.
	require.NoError(t, db.Model(plan).Update("max_backups", 0).Error)
	ok, _, _ = s.scheduledBackupAllowed(ctx, vm)
	assert.False(t, ok)
	require.NoError(t, s.pruneOverAllowance(ctx, "vm-a"))
	assert.Len(t, remaining(), 3)
}
