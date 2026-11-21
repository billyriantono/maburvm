//go:build pg

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// 075 applies through the runner, again on itself, down and up; a backup's
// disks round-trip through jsonb, also by the column-limited update the
// backup worker writes a finished backup with.
func TestBackupDisksMigration(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn))
	up, down := migrationSQL(t, "075_backup_disks.up.sql"), migrationSQL(t, "075_backup_disks.down.sql")
	for _, step := range []struct{ name, stmt string }{{"075 again", up}, {"075 down", down}, {"075 up after down", up}} {
		_, err := raw.Exec(step.stmt)
		require.NoError(t, err, step.name)
	}

	for _, stmt := range []string{
		`INSERT INTO users (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111', 'alice@example.com', 'x')`,
		`INSERT INTO nodes (id, name, ip_address, token) VALUES ('33333333-3333-3333-3333-333333333333', 'n1', '192.0.2.1', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('44444444-4444-4444-4444-444444444444', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('55555555-5555-5555-5555-555555555555',
			'11111111-1111-1111-1111-111111111111', '33333333-3333-3333-3333-333333333333', 'web-1', '44444444-4444-4444-4444-444444444444')`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}
	vm := &models.VM{ID: "55555555-5555-5555-5555-555555555555"}

	b := &models.Backup{VMID: vm.ID, StorageProvider: models.BackupStoreDestination, StoragePath: "backups/x.qcow2"}
	require.NoError(t, db.Create(b).Error)
	var got models.Backup
	require.NoError(t, db.First(&got, "id = ?", b.ID).Error)
	require.Empty(t, got.Disks)
	require.Empty(t, got.Consistency)

	done := &models.Backup{Status: models.BackupStatusCompleted, Size: 15, Compression: "zstd", Consistency: models.BackupConsistencyFrozen,
		Disks: []models.BackupDisk{{Target: "vda", Key: "backups/x.qcow2", Size: 10}, {Target: "vdb", Key: "backups/x-vdb.qcow2", Size: 5, VirtualSize: 1 << 30}}}
	require.NoError(t, db.Model(&models.Backup{ID: b.ID}).
		Select("status", "size", "compression", "consistency", "disks", "error_message").Updates(done).Error)
	require.NoError(t, db.First(&got, "id = ?", b.ID).Error)
	require.Equal(t, done.Disks, got.Disks)
	require.Equal(t, models.BackupConsistencyFrozen, got.Consistency)
	require.Equal(t, models.BackupStatusCompleted, got.Status)

	// A backup from an older agent has no disk list: written as empty, not refused.
	done.Disks = nil
	require.NoError(t, db.Model(&models.Backup{ID: b.ID}).Select("disks").Updates(done).Error)
	require.NoError(t, db.First(&got, "id = ?", b.ID).Error)
	require.Empty(t, got.Disks)
}
