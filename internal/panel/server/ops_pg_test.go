//go:build pg

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/queue"
)

const (
	opsAlice = "11111111-1111-1111-1111-111111111111"
	opsBob   = "22222222-2222-2222-2222-222222222222"
	opsVM1   = "55555555-5555-5555-5555-555555555555"
	opsVM2   = "66666666-6666-6666-6666-666666666666"
)

func seedOps(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO users (id, email, password_hash, role) VALUES ('` + opsAlice + `', 'alice@example.com', 'x', 'client')`,
		`INSERT INTO users (id, email, password_hash, role) VALUES ('` + opsBob + `', 'bob@example.com', 'x', 'client')`,
		`INSERT INTO nodes (id, name, ip_address, token) VALUES ('33333333-3333-3333-3333-333333333333', 'n1', '192.0.2.1', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('44444444-4444-4444-4444-444444444444', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('` + opsVM1 + `', '` + opsAlice + `',
			'33333333-3333-3333-3333-333333333333', 'web-1', '44444444-4444-4444-4444-444444444444')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('` + opsVM2 + `', '` + opsBob + `',
			'33333333-3333-3333-3333-333333333333', 'db-1', '44444444-4444-4444-4444-444444444444')`,
		// A disk attached long ago: its consumed reservation names (VM, owner).
		`INSERT INTO disk_quota_reservations (user_id, vm_id, size_gb, status, consumed_at) VALUES ('` + opsAlice + `', '` + opsVM1 + `', 10, 'consumed', NOW())`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}
}

// 076 applies through the runner, again on itself, down and up.
func TestEmailLogAndNotificationsMigration(t *testing.T) {
	_, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn))
	up, down := migrationSQL(t, "076_email_log_and_notifications.up.sql"), migrationSQL(t, "076_email_log_and_notifications.down.sql")
	for _, step := range []struct{ name, sql string }{{"076 again", up}, {"076 down", down}, {"076 up after down", up}} {
		_, err := raw.Exec(step.sql)
		require.NoError(t, err, step.name)
	}
}

// Deleting an owner hands its VMs over with their disk reservations, which a
// foreign key ties to the owner, and refuses a new owner with a VM of the same name.
func TestReassignUserVMs(t *testing.T) {
	db, _, _ := pgMigratedDB(t)
	seedOps(t, db)
	ctx := context.Background()

	var fk int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_constraint WHERE conname = 'fk_disk_reservation_vm_user'`).Scan(&fk).Error)
	require.EqualValues(t, 1, fk, "the composite (vm, owner) key this test is about")

	owned, err := service.UserOwnedVMs(ctx, db, opsAlice)
	require.NoError(t, err)
	require.Len(t, owned, 1)

	require.NoError(t, db.Exec(`UPDATE vms SET hostname = 'web-1' WHERE id = ?`, opsVM2).Error)
	err = db.Transaction(func(tx *gorm.DB) error { _, err := service.ReassignUserVMs(ctx, tx, opsAlice, opsBob); return err })
	require.ErrorIs(t, err, service.ErrReassignHostnames)
	assert.Contains(t, err.Error(), "web-1")
	require.NoError(t, db.Exec(`UPDATE vms SET hostname = 'db-1' WHERE id = ?`, opsVM2).Error)

	err = db.Transaction(func(tx *gorm.DB) error { _, err := service.ReassignUserVMs(ctx, tx, opsAlice, opsAlice); return err })
	require.ErrorIs(t, err, service.ErrReassignTarget)

	var moved []service.OwnedVM
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		moved, err = service.ReassignUserVMs(ctx, tx, opsAlice, opsBob)
		return err
	}))
	require.Len(t, moved, 1)
	var owner, resOwner string
	require.NoError(t, db.Raw(`SELECT user_id::text FROM vms WHERE id = ?`, opsVM1).Scan(&owner).Error)
	require.NoError(t, db.Raw(`SELECT user_id::text FROM disk_quota_reservations WHERE vm_id = ?`, opsVM1).Scan(&resOwner).Error)
	assert.Equal(t, opsBob, owner)
	assert.Equal(t, opsBob, resOwner)
}

// A VM's owner gets a notification for a lifecycle event, and a customer one
// for a failed backup; the account an administrator owns gets no backup notice.
func TestVMNoticesOnPostgres(t *testing.T) {
	db, _, _ := pgMigratedDB(t)
	seedOps(t, db)
	ctx := context.Background()

	require.NoError(t, service.NotifyVMOwner(ctx, db, opsVM1, service.EmailVMSuspended))
	require.NoError(t, service.NotifyBackupFailed(ctx, db, []byte(`{"vm_id":"`+opsVM1+`","scheduled":true}`)))
	rows, unread, err := service.ListNotifications(ctx, db, opsAlice, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.EqualValues(t, 2, unread)
	assert.Equal(t, "vm", rows[0].ResourceType)
	assert.Equal(t, opsVM1, rows[0].ResourceID)
	titles := rows[0].Title + "|" + rows[1].Title
	assert.Contains(t, titles, "Your server web-1 has been suspended")
	assert.Contains(t, titles, "A backup of your server web-1 failed")

	// A failed create is a notice; a failed start is staff's alone.
	require.NoError(t, service.NotifyVMOperationFailed(ctx, db, []byte(`{"vm_id":"`+opsVM1+`","operation":"create"}`)))
	require.NoError(t, service.NotifyVMOperationFailed(ctx, db, []byte(`{"vm_id":"`+opsVM1+`","operation":"start"}`)))
	rows, _, err = service.ListNotifications(ctx, db, opsAlice, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Contains(t, rows[0].Title+rows[1].Title+rows[2].Title, "Your server web-1 could not be created")

	require.NoError(t, db.Exec(`UPDATE users SET role = 'admin' WHERE id = ?`, opsBob).Error)
	require.NoError(t, service.NotifyBackupFailed(ctx, db, []byte(`{"vm_id":"`+opsVM2+`"}`)))
	require.NoError(t, service.NotifyVMOperationFailed(ctx, db, []byte(`{"vm_id":"`+opsVM2+`","operation":"rebuild"}`)))
	rows, _, err = service.ListNotifications(ctx, db, opsBob, 10)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// Every group of operational gauges reads on a real schema, River's included.
func TestOpsCollectorOnPostgres(t *testing.T) {
	db, _, dsn := pgMigratedDB(t)
	seedOps(t, db)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, queue.RunMigrations(ctx, pool))
	for _, q := range []string{
		`INSERT INTO river_job (args, kind, max_attempts, queue, state, scheduled_at) VALUES ('{}', 'vm_operation', 5, 'critical', 'available', NOW() - interval '90 seconds')`,
		`INSERT INTO river_job (args, kind, max_attempts, queue, state, finalized_at) VALUES ('{}', 'backup', 3, 'batch', 'discarded', NOW())`,
		`INSERT INTO backups (vm_id, storage_provider, storage_path, status, completed_at) VALUES ('` + opsVM1 + `', 's3', 'a', 'completed', NOW() - interval '1 day')`,
		`INSERT INTO backups (vm_id, storage_provider, storage_path, status) VALUES ('` + opsVM1 + `', 's3', 'b', 'failed')`,
		`INSERT INTO storage_pools (name, type, path, node_id, total_space, available_space) VALUES ('vm', 'dir', '/vm', '33333333-3333-3333-3333-333333333333', 1000, 400)`,
		`INSERT INTO alerts (alert_key, kind, severity, state, title) VALUES ('k', 'node_offline', 'critical', 'firing', 't')`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(&opsCollector{db: db})
	families, err := reg.Gather()
	require.NoError(t, err)
	got := map[string][]*dto.Metric{}
	for _, f := range families {
		got[f.GetName()] = f.GetMetric()
	}
	for _, m := range got["maburvm_ops_collect_success"] {
		assert.EqualValues(t, 1, m.GetGauge().GetValue(), "group %v", m.GetLabel())
	}
	require.Len(t, got["maburvm_ops_collect_success"], 5)
	value := func(name string, labels ...string) float64 {
		for _, m := range got[name] {
			s := ""
			for _, l := range m.GetLabel() {
				s += l.GetName() + "=" + l.GetValue() + ","
			}
			match := true
			for _, want := range labels {
				match = match && strings.Contains(s, want+",")
			}
			if match {
				return m.GetGauge().GetValue()
			}
		}
		t.Fatalf("%s %v not found", name, labels)
		return 0
	}
	assert.EqualValues(t, 1, value("maburvm_queue_jobs", "kind=vm_operation", "state=available"))
	assert.GreaterOrEqual(t, value("maburvm_queue_oldest_available_job_age_seconds", "queue=critical"), 80.0)
	assert.EqualValues(t, 1, value("maburvm_queue_jobs_finished_recently", "kind=backup", "state=discarded"))
	assert.EqualValues(t, 1, value("maburvm_backup_last_failed", "vm=web-1"))
	assert.Greater(t, value("maburvm_backup_last_success_timestamp_seconds", "vm=web-1"), 0.0)
	assert.EqualValues(t, 1000, value("maburvm_storage_pool_capacity_bytes", "pool=vm"))
	assert.EqualValues(t, 400, value("maburvm_storage_pool_available_bytes", "pool=vm"))
	assert.EqualValues(t, 1, value("maburvm_alerts_firing", "kind=node_offline", "severity=critical"))
}
