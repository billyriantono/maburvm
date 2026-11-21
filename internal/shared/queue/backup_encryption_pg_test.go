//go:build pg

package queue

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sharedb "github.com/maburvm/panel/internal/shared/db"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// Migration 092 goes down and up cleanly, and the backup worker's update of a
// finished backup, which names encryption_key_id, works on the real table.
func TestBackupEncryptionMigrationOnPostgres(t *testing.T) {
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	ctx := context.Background()
	name := fmt.Sprintf("maburvm_bkenc_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	admin.Close()
	require.NoError(t, err)
	dsn := dsnForDatabase(t, base, name)
	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		raw.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
			a.Close()
		}
	})
	require.NoError(t, sharedb.EnsureMigrationsTable(ctx, raw))
	_, err = sharedb.Up(ctx, raw, sharedb.MigrationsFS, "migrations", 5*time.Minute, nil)
	require.NoError(t, err)
	for _, f := range []string{"092_backup_encryption.down.sql", "092_backup_encryption.up.sql", "092_backup_encryption.up.sql"} {
		b, err := fs.ReadFile(sharedb.MigrationsFS, "migrations/"+f)
		require.NoError(t, err)
		_, err = raw.Exec(string(b))
		require.NoError(t, err, f)
	}

	var userID, nodeID, tmplID, vmID, backupID string
	require.NoError(t, raw.QueryRow(`INSERT INTO users (name, email, password_hash, role) VALUES ('u', 'u@example.com', 'x', 'client') RETURNING id`).Scan(&userID))
	require.NoError(t, raw.QueryRow(`INSERT INTO nodes (name, ip_address, token) VALUES ('n', '192.0.2.250', 'tok') RETURNING id`).Scan(&nodeID))
	require.NoError(t, raw.QueryRow(`INSERT INTO os_templates (name, version, image_path) VALUES ('debian', '12', '/i.qcow2') RETURNING id`).Scan(&tmplID))
	require.NoError(t, raw.QueryRow(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES ($1, $2, 'v', $3, '{"cpu":1,"ram":512,"disk":10}') RETURNING id`,
		userID, nodeID, tmplID).Scan(&vmID))
	require.NoError(t, raw.QueryRow(`INSERT INTO backups (vm_id, storage_provider, storage_path) VALUES ($1, 'destination', 'backups/x.qcow2') RETURNING id`, vmID).Scan(&backupID))
	var dest string
	require.NoError(t, raw.QueryRow(`INSERT INTO backup_destinations (name, kind, config, secret_enc) VALUES ('d', 's3', '{}', 'x') RETURNING id`).Scan(&dest))
	var keys string
	var encrypt bool
	require.NoError(t, raw.QueryRow(`SELECT encrypt, encryption_keys::text FROM backup_destinations WHERE id = $1`, dest).Scan(&encrypt, &keys))
	require.False(t, encrypt, "an existing destination does not start encrypting")
	require.Equal(t, "[]", keys)

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	done := completedBackup(&pb.BackupDiskResponse{Disks: []*pb.BackupDiskFile{{Target: "vda", Key: "backups/x.qcow2", SizeBytes: 9, Checksum: "c",
		EncryptionKeyId: "00000000-0000-4000-8000-000000000001"}}})
	require.NoError(t, gdb.Model(&models.Backup{ID: backupID}).Select("status", "size", "checksum", "disks", "encryption_key_id").Updates(done).Error)
	var got models.Backup
	require.NoError(t, gdb.First(&got, "id = ?", backupID).Error)
	require.Equal(t, "00000000-0000-4000-8000-000000000001", got.EncryptionKeyID)
}
