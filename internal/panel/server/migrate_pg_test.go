//go:build pg

package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sharedb "github.com/maburvm/panel/internal/shared/db"
)

// With MIGRATION_REQUIRE_BACKUP_WITHIN set, a pending migration on a database
// with accounts in it waits for a recent backup marker; one without does not.
func TestRunMigrationsRequiresRecentBackupMarker(t *testing.T) {
	_, raw, dsn := pgMigratedDB(t)

	dir := t.TempDir()
	entries, err := fs.ReadDir(sharedb.MigrationsFS, "migrations")
	require.NoError(t, err)
	for _, e := range entries {
		b, err := fs.ReadFile(sharedb.MigrationsFS, "migrations/"+e.Name())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600))
	}
	t.Setenv("MIGRATIONS_DIR", dir)
	t.Setenv("MIGRATION_REQUIRE_BACKUP_WITHIN", "1h")

	// Nobody has an account yet (a first boot that stopped part way): nothing to lose.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "998_before_any_user.up.sql"), []byte("CREATE TABLE before_any_user (id int)"), 0o600))
	require.NoError(t, runMigrations(dsn))

	_, err = raw.Exec(`INSERT INTO users (name, email, password_hash, role) VALUES ('Owner', 'owner@example.com', 'x', 'admin')`)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "999_backup_marker_test.up.sql"), []byte("CREATE TABLE backup_marker_test (id int)"), 0o600))
	err = runMigrations(dsn)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "999_backup_marker_test") && strings.Contains(err.Error(), "none is recorded"), err.Error())

	_, err = raw.Exec(`INSERT INTO system_settings (section, data, updated_at) VALUES ($1, $2::jsonb, NOW())`,
		BackupMarkerSection, `{"takenAt":"`+time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339)+`","file":"backups/old.dump"}`)
	require.NoError(t, err)
	err = runMigrations(dsn)
	require.Error(t, err, "a three-hour-old backup does not count for 1h")

	_, err = raw.Exec(`UPDATE system_settings SET data = $2::jsonb WHERE section = $1`,
		BackupMarkerSection, `{"takenAt":"`+time.Now().UTC().Format(time.RFC3339)+`","file":"backups/new.dump"}`)
	require.NoError(t, err)
	require.NoError(t, runMigrations(dsn))
	var reg *string
	require.NoError(t, raw.QueryRow(`SELECT to_regclass('backup_marker_test')::text`).Scan(&reg))
	require.NotNil(t, reg, "the pending migration applied once a fresh backup was recorded")
}
