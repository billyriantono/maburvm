package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFleetBackupOverview(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ago := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT)`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, hostname TEXT, status TEXT, user_id TEXT, node_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE backup_schedules (id TEXT PRIMARY KEY, vm_id TEXT, schedule TEXT, status TEXT, storage_provider TEXT, compression TEXT, retention_policy TEXT, next_run_at DATETIME, last_run_at DATETIME, last_backup_id TEXT, full_every_days INTEGER DEFAULT 0, chain_reset_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`INSERT INTO users VALUES ('u1', 'ana@example.test'), ('u2', 'bo@example.test')`,
		`INSERT INTO nodes (id, name, status) VALUES ('n1', 'node-a', 'active'), ('n2', 'node-b', 'active')`,
		`INSERT INTO vms (id, hostname, status, user_id, node_id) VALUES
			('ok', 'web', 'running', 'u1', 'n1'), ('fail', 'db', 'running', 'u1', 'n1'),
			('stale', 'mail', 'running', 'u2', 'n2'), ('none', 'test', 'stopped', 'u2', 'n2')`,
		`INSERT INTO backup_schedules (id, vm_id, schedule, status, full_every_days, next_run_at) VALUES
			('s1', 'ok', '0 2 * * *', 'active', 7, '2026-11-21 02:00:00'), ('s2', 'fail', '0 2 * * *', 'active', 0, NULL),
			('s3', 'stale', '0 3 * * *', 'active', 0, NULL)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	d := NewBackupDestinations(db, nil, testAESKey)
	dest := createDest(t, d, validDestination())
	type b struct {
		id, vm, status, mode string
		size                 int64
		at                   time.Time
		dest                 any
		errMsg               string
	}
	for _, r := range []b{
		{"b1", "ok", "completed", "full", 100, ago(30), dest.ID, ""},
		{"b2", "ok", "completed", "incremental", 10, ago(6), dest.ID, ""},
		{"b3", "fail", "completed", "full", 50, ago(30), dest.ID, ""},
		{"b4", "fail", "failed", "full", 0, ago(5), nil, "upload failed: refused"},
		{"b5", "stale", "completed", "full", 70, ago(24 * 5), nil, ""},
		{"b6", "stale", "pending", "full", 0, ago(1), nil, ""},
		{"b7", "ok", "in_progress", "incremental", 0, ago(1), nil, ""},
	} {
		require.NoError(t, db.Exec(`INSERT INTO backups (id, vm_id, status, mode, size, created_at, completed_at, updated_at, destination_id, error_message, storage_provider, storage_path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 's3', 'k')`, r.id, r.vm, r.status, r.mode, r.size, r.at, r.at, r.at, r.dest, r.errMsg).Error)
	}

	o, err := FleetBackupOverview(ctx, db, BackupOverviewFilter{}, now)
	require.NoError(t, err)
	order := []string{}
	for _, r := range o.Rows {
		order = append(order, r.VMID+":"+r.State)
	}
	require.Equal(t, []string{"fail:failing", "stale:stale", "none:unscheduled", "ok:ok"}, order)
	require.Equal(t, map[string]int{"failing": 1, "stale": 1, "unscheduled": 1, "ok": 1}, o.Counts)

	ok := o.Rows[3]
	require.Equal(t, "web", ok.Hostname)
	require.Equal(t, "ana@example.test", ok.OwnerEmail)
	require.Equal(t, "node-a", ok.NodeName)
	require.Equal(t, 1, ok.Fulls)
	require.Equal(t, 1, ok.Incrementals)
	require.Equal(t, 1, ok.Running)
	require.EqualValues(t, 110, ok.StoredBytes)
	require.EqualValues(t, 10, ok.LastOKSize)
	require.Equal(t, "incremental", ok.LastOKMode)
	require.Equal(t, 7, ok.FullEveryDays)
	require.NotNil(t, ok.NextRun)

	fail := o.Rows[0]
	require.Equal(t, "failed", fail.LastResult)
	require.Equal(t, "upload failed: refused", fail.LastError)
	require.NotNil(t, fail.LastOK, "its last success is still shown")

	// Filters narrow the rows; the counts say what each state holds.
	o, err = FleetBackupOverview(ctx, db, BackupOverviewFilter{NodeID: "n2", State: "stale"}, now)
	require.NoError(t, err)
	require.Len(t, o.Rows, 1)
	require.Equal(t, "stale", o.Rows[0].VMID)
	require.Equal(t, 1, o.Rows[0].Queued)
	o, err = FleetBackupOverview(ctx, db, BackupOverviewFilter{Query: "BO@"}, now)
	require.NoError(t, err)
	require.Len(t, o.Rows, 2)
	// A longer stale window makes the mail server fine again.
	o, err = FleetBackupOverview(ctx, db, BackupOverviewFilter{UserID: "u2", StaleDays: 7}, now)
	require.NoError(t, err)
	require.Equal(t, "ok", o.Rows[1].State)

	require.Len(t, o.Destinations, 2)
	require.Equal(t, dest.ID, o.Destinations[0].ID)
	require.EqualValues(t, 3, o.Destinations[0].Backups)
	require.EqualValues(t, 160, o.Destinations[0].Bytes)
	require.Equal(t, "node", o.Destinations[1].Kind)
	require.EqualValues(t, 70, o.Destinations[1].Bytes)

	loads := map[string]BackupNodeLoad{}
	for _, n := range o.Nodes {
		loads[n.Name] = n
	}
	require.Equal(t, 1, loads["node-a"].Running)
	require.Equal(t, 1, loads["node-b"].Queued)
	require.Equal(t, 2, loads["node-a"].Limit)
}
