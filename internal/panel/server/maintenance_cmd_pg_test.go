//go:build pg

package server

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedb "github.com/maburvm/panel/internal/shared/db"
	"github.com/maburvm/panel/internal/shared/queue"
)

// The command scripts/upgrade-panel.sh runs, against a real database: on holds
// the job queue and waits for running jobs (exit 3 while one still runs), off
// releases the queue.
func TestMaintenanceCommandOnPostgres(t *testing.T) {
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	u, err := url.Parse(base)
	require.NoError(t, err)
	name := "maburvm_maint_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = admin.Exec("CREATE DATABASE " + name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
		_ = admin.Close()
	})
	u.Path = "/" + name
	ctx := context.Background()
	raw, err := sql.Open("pgx", u.String())
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, sharedb.EnsureMigrationsTable(ctx, raw))
	_, err = sharedb.Up(ctx, raw, sharedb.MigrationsFS, "migrations", time.Minute, nil)
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, u.String())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, queue.RunMigrationsWithLogger(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))))

	// The command reads the panel's own configuration from the environment.
	user := u.User.Username()
	if user == "" {
		user = "postgres"
	}
	pass, _ := u.User.Password()
	host, port := u.Hostname(), u.Port()
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	t.Setenv("DB_HOST", host)
	t.Setenv("DB_PORT", port)
	t.Setenv("DB_USER", user)
	t.Setenv("DB_PASSWORD", pass)
	t.Setenv("DB_NAME", name)
	t.Setenv("DB_SSL_MODE", "disable")
	// The JWT secret is TestMain's: the process caches the first one resolved,
	// and the other tests mint tokens with it.
	if os.Getenv("AES_KEY") == "" {
		t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	old := maintenancePoll
	maintenancePoll = 100 * time.Millisecond
	defer func() { maintenancePoll = old }()

	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := MaintenanceCommand(args, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	paused := func() int {
		var n int
		require.NoError(t, raw.QueryRow("SELECT COUNT(*) FROM river_queue WHERE paused_at IS NOT NULL").Scan(&n))
		return n
	}
	_, err = raw.Exec(`INSERT INTO river_queue (name, created_at, updated_at) VALUES ('critical', now(), now()), ('batch', now(), now())`)
	require.NoError(t, err)

	code, out, errOut := run("on", "--hold-jobs", "--wait", "5", "--message", "Upgrading")
	require.Equal(t, 0, code, out+errOut)
	assert.Contains(t, out, "no job is running")
	assert.Equal(t, 2, paused(), "every queue is held")

	_, err = raw.Exec(`INSERT INTO river_job (kind, args, state, queue, attempt, max_attempts, priority, attempted_at)
		VALUES ('backup', '{"vm_id":"x"}', 'running', 'batch', 1, 3, 1, now())`)
	require.NoError(t, err)
	code, _, errOut = run("on", "--hold-jobs", "--wait", "0")
	assert.Equal(t, 0, code, "--wait 0 does not wait")
	code, _, errOut = run("on", "--hold-jobs", "--wait", "1")
	assert.Equal(t, 3, code, "a job still running after the wait")
	assert.Contains(t, errOut, "backup")

	code, out, _ = run("status")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "maintenance: on")
	assert.Contains(t, out, "running jobs: 1")

	code, _, errOut = run("off")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, 0, paused(), "the queues are released")
	code, out, _ = run("status")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "maintenance: off")
}
