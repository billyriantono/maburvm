//go:build pg

package service

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sharedb "github.com/maburvm/panel/internal/shared/db"
	"github.com/maburvm/panel/internal/shared/models"
)

// A panel upgraded with an S3 destination saved in system settings keeps
// using it: migration 082 makes it the default, and the backups already in it
// point at it.
func TestMigrationCopiesTheSavedDestinationOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	enc, err := encryptWithKey([]byte(testAESKey), "legacy-secret")
	require.NoError(t, err)
	legacy, _ := json.Marshal(map[string]any{"endpoint": "s3.example.test", "region": "auto", "bucket": "old-bucket",
		"path_prefix": "prod", "access_key": "AK", "secret_key_enc": enc, "plain_http": false})
	seed := []string{
		`INSERT INTO users (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111', 'ana@example.test', 'x')`,
		`INSERT INTO nodes (id, name, ip_address, token) VALUES ('33333333-3333-3333-3333-333333333333', 'node-a', '192.0.2.1', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('44444444-4444-4444-4444-444444444444', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('55555555-5555-5555-5555-555555555555', '11111111-1111-1111-1111-111111111111', '33333333-3333-3333-3333-333333333333', 'web', '44444444-4444-4444-4444-444444444444')`,
		`INSERT INTO backups (id, vm_id, storage_provider, storage_path, status, size, completed_at) VALUES
			('66666666-6666-6666-6666-000000000001', '55555555-5555-5555-5555-555555555555', 's3', 'prod/backups/a.qcow2', 'completed', 100, NOW() - INTERVAL '1 hour'),
			('66666666-6666-6666-6666-000000000002', '55555555-5555-5555-5555-555555555555', 'node', 'backups/b.qcow2', 'completed', 50, NOW() - INTERVAL '2 hours')`,
	}
	for _, s := range seed {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('backup_destination', ?::jsonb)`, string(legacy)).Error)

	up, err := fs.ReadFile(sharedb.MigrationsFS, "migrations/082_backup_chains_destinations.up.sql")
	require.NoError(t, err)
	// Applied again over the now-saved row; it must copy once and only once.
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Exec(string(up)).Error)
	}

	d := NewBackupDestinations(db, nil, testAESKey)
	list, err := d.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].IsDefault)
	require.Equal(t, "old-bucket", list[0].Bucket)
	require.Equal(t, "prod", list[0].PathPrefix)
	require.EqualValues(t, 1, list[0].Backups)
	require.EqualValues(t, 100, list[0].BackupBytes)
	store, id, err := d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, list[0].ID, id)
	require.Equal(t, "legacy-secret", store.GetSecretKey(), "the secret is still readable with the panel's key")

	var b models.Backup
	require.NoError(t, db.First(&b, "id = ?", "66666666-6666-6666-6666-000000000001").Error)
	require.Equal(t, id, strVal(b.DestinationID))
	require.Equal(t, models.BackupModeFull, b.Mode)
	var nodeKept models.Backup
	require.NoError(t, db.First(&nodeKept, "id = ?", "66666666-6666-6666-6666-000000000002").Error)
	require.Nil(t, nodeKept.DestinationID, "a backup in a node's own storage is not moved into the destination")

	// New destinations and the default switch work on Postgres too.
	p, err := d.Create(ctx, BackupDestinationInput{Kind: "path", Path: "/mnt/backups"})
	require.NoError(t, err)
	require.False(t, p.IsDefault)
	require.NoError(t, d.SetDefault(ctx, p.ID))
	_, id, err = d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, p.ID, id)

	// The fleet page reads the same rows.
	require.NoError(t, db.Exec(`INSERT INTO backup_schedules (vm_id, schedule, storage_provider, full_every_days, next_run_at) VALUES ('55555555-5555-5555-5555-555555555555', '0 2 * * *', 's3', 7, NOW() + INTERVAL '1 day')`).Error)
	o, err := FleetBackupOverview(ctx, db, BackupOverviewFilter{}, time.Now())
	require.NoError(t, err)
	require.Len(t, o.Rows, 1)
	r := o.Rows[0]
	require.Equal(t, BackupStateOK, r.State)
	require.Equal(t, "ana@example.test", r.OwnerEmail)
	require.Equal(t, "node-a", r.NodeName)
	require.Equal(t, 2, r.Fulls)
	require.EqualValues(t, 150, r.StoredBytes)
	require.Equal(t, 7, r.FullEveryDays)
	require.Len(t, o.Destinations, 3, "both destinations and the nodes' own storage")
	require.Len(t, o.Nodes, 1)

	// Chain fields round-trip through GORM, and the chain is read back from them.
	svc := &BackupService{db: db}
	parent := "66666666-6666-6666-6666-000000000001"
	require.NoError(t, db.Model(&models.Backup{ID: parent}).Updates(map[string]any{
		"checkpoint": "maburvm-" + parent, "chain_id": parent, "node_id": "33333333-3333-3333-3333-333333333333", "backup_type": "scheduled"}).Error)
	all, err := svc.vmBackups(ctx, "55555555-5555-5555-5555-555555555555")
	require.NoError(t, err)
	dec := decideChain(chainInput{Now: time.Now(), FullEveryDays: 7, DestinationID: list[0].ID, NodeID: "33333333-3333-3333-3333-333333333333", Backups: all})
	require.Equal(t, parent, dec.ParentID, dec.Why)
	require.Equal(t, "maburvm-"+parent, dec.From)
}
