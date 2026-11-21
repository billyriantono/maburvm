package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
	sharedb "github.com/maburvm/panel/internal/shared/db"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	// defaultMigrationTimeout is per migration: one that rewrites a large table
	// must not be cut off because the ones before it took a while.
	defaultMigrationTimeout = 30 * time.Minute
	// BackupMarkerSection is the system_settings row scripts/upgrade-panel.sh
	// writes after it dumps the database: {"takenAt": RFC3339, "file": "..."}.
	BackupMarkerSection = "pre_migration_backup"
)

// migrationOptions reads MIGRATION_TIMEOUT and MIGRATION_REQUIRE_BACKUP_WITHIN.
// A value that does not parse stops the boot rather than silently using a default.
func migrationOptions() (timeout, requireBackup time.Duration, err error) {
	timeout = defaultMigrationTimeout
	if v := strings.TrimSpace(os.Getenv("MIGRATION_TIMEOUT")); v != "" {
		if timeout, err = time.ParseDuration(v); err != nil || timeout <= 0 {
			return 0, 0, fmt.Errorf("MIGRATION_TIMEOUT=%q is not a positive duration such as 30m", v)
		}
	}
	if v := strings.TrimSpace(os.Getenv("MIGRATION_REQUIRE_BACKUP_WITHIN")); v != "" && v != "0" {
		if requireBackup, err = time.ParseDuration(v); err != nil || requireBackup < 0 {
			return 0, 0, fmt.Errorf("MIGRATION_REQUIRE_BACKUP_WITHIN=%q is not a duration such as 6h", v)
		}
	}
	return timeout, requireBackup, nil
}

// runMigrations applies all pending SQL migrations on startup. Migrations are
// embedded in the binary (so they apply regardless of the working directory);
// set MIGRATIONS_DIR to apply on-disk migrations instead (e.g. new files
// without a rebuild).
func runMigrations(dsn string) error {
	timeout, requireBackup, err := migrationOptions()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	if err := sharedb.EnsureMigrationsTable(ctx, db); err != nil {
		return err
	}

	fsys, root := migrationsSource()
	status, err := sharedb.Status(ctx, db, fsys, root)
	if err != nil {
		// Do NOT skip silently: a missing migration source is an operational error
		// that would otherwise leave the schema half-built.
		return fmt.Errorf("failed to read migrations from %q: %w", root, err)
	}
	if len(status) == 0 {
		slog.Default().Warn("no migrations found", "root", root)
		return nil
	}
	pending := sharedb.Pending(status)
	if len(pending) == 0 {
		slog.Default().Info("migrations up to date", "applied_now", 0, "total", len(status))
		return nil
	}
	// A fresh database has nothing a backup could save, even half built by a
	// first boot that stopped part way.
	if requireBackup > 0 && holdsData(ctx, db) {
		if err := checkBackupMarker(ctx, db, requireBackup, time.Now()); err != nil {
			return fmt.Errorf("refusing to apply %d pending migrations (first %s): %w", len(pending), pending[0], err)
		}
	}

	slog.Default().Info("applying migrations", "pending", len(pending), "first", pending[0], "timeout_each", timeout.String())
	applied, err := sharedb.Up(context.Background(), db, fsys, root, timeout, func(v string, took time.Duration) {
		slog.Default().Info("applied migration", "version", v, "took", took.Round(time.Millisecond).String())
	})
	if err != nil {
		return err
	}
	slog.Default().Info("migrations up to date", "applied_now", applied, "total", len(status))
	return nil
}

// holdsData reports whether anyone has an account yet. When it cannot tell, it
// says yes, so the backup requirement is not skipped by mistake.
func holdsData(ctx context.Context, db *sql.DB) bool {
	var reg sql.NullString
	if db.QueryRowContext(ctx, `SELECT to_regclass('users')::text`).Scan(&reg) != nil {
		return true
	}
	if !reg.Valid {
		return false
	}
	var found bool
	if db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&found) != nil {
		return true
	}
	return found
}

// lastBackup reads when the newest pre-migration backup was recorded; zero
// when none was.
func lastBackup(ctx context.Context, db *sql.DB) (time.Time, string) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT CAST(data AS TEXT) FROM system_settings WHERE section = $1`, BackupMarkerSection).Scan(&raw); err != nil {
		return time.Time{}, ""
	}
	var m struct {
		TakenAt time.Time `json:"takenAt"`
		File    string    `json:"file"`
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return time.Time{}, ""
	}
	return m.TakenAt, m.File
}

func checkBackupMarker(ctx context.Context, db *sql.DB, within time.Duration, now time.Time) error {
	at, file := lastBackup(ctx, db)
	return backupFresh(at, file, within, now)
}

// backupFresh says why a recorded backup does not count, or nil when it does.
func backupFresh(at time.Time, file string, within time.Duration, now time.Time) error {
	hint := "Run scripts/upgrade-panel.sh, which takes one, or unset MIGRATION_REQUIRE_BACKUP_WITHIN (docs/DEPLOYMENT.md, Upgrading the panel)"
	if at.IsZero() {
		return fmt.Errorf("MIGRATION_REQUIRE_BACKUP_WITHIN=%s needs a database backup taken first, and none is recorded. %s", within, hint)
	}
	if age := now.Sub(at); age > within {
		return fmt.Errorf("MIGRATION_REQUIRE_BACKUP_WITHIN=%s needs a database backup taken in that time; the newest recorded (%s) is %s old. %s",
			within, file, age.Round(time.Minute), hint)
	}
	return nil
}

// migrationsSource returns the embedded migrations by default, or an on-disk
// directory when MIGRATIONS_DIR points to a real directory.
func migrationsSource() (fs.FS, string) {
	if dir := strings.TrimSpace(os.Getenv("MIGRATIONS_DIR")); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return os.DirFS(dir), "."
		}
		slog.Default().Warn("MIGRATIONS_DIR is set but not a directory; falling back to embedded migrations", "dir", dir)
	}
	return sharedb.MigrationsFS, "migrations"
}

const migrateUsage = `usage: migrate [up | status | down N --yes]
  up         apply pending migrations (what the panel does when it starts)
  status     list migrations, which are applied, and the newest pre-migration backup
  down N     revert the N most recently applied migrations; prints them and changes
             nothing unless --yes is given. Never run automatically.
`

// MigrateCommand is "panel migrate ..." and cmd/migrate. It returns the exit code.
func MigrateCommand(args []string, stdout, stderr io.Writer) int {
	cmd := "up"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	n, yes := 0, false
	switch cmd {
	case "up", "status":
		if len(args) > 0 {
			fmt.Fprint(stderr, migrateUsage)
			return 2
		}
	case "down":
		for _, a := range args {
			if a == "--yes" {
				yes = true
			} else if v, err := strconv.Atoi(a); err == nil && n == 0 {
				n = v
			} else {
				fmt.Fprint(stderr, migrateUsage)
				return 2
			}
		}
		if n < 1 {
			fmt.Fprint(stderr, migrateUsage)
			return 2
		}
	case "help", "-h", "--help":
		fmt.Fprint(stdout, migrateUsage)
		return 0
	default:
		fmt.Fprint(stderr, migrateUsage)
		return 2
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintln(stderr, "migrate: failed to load config:", err)
		return 1
	}
	dsn := databaseURL(cfg.Database)
	if cmd == "up" {
		if err := runMigrations(dsn); err != nil {
			fmt.Fprintln(stderr, "migrate:", err)
			return 1
		}
		return 0
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	if err := sharedb.EnsureMigrationsTable(ctx, db); err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	fsys, root := migrationsSource()
	if cmd == "status" {
		ms, err := sharedb.Status(ctx, db, fsys, root)
		if err != nil {
			fmt.Fprintln(stderr, "migrate:", err)
			return 1
		}
		at, file := lastBackup(ctx, db)
		printStatus(stdout, ms, at, file, time.Now())
		return 0
	}

	plan, err := sharedb.DownPlan(ctx, db, fsys, root, n)
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	if !yes {
		fmt.Fprintf(stdout, "These %d migrations would be reverted, newest first:\n", len(plan))
		for _, v := range plan {
			fmt.Fprintln(stdout, "  "+v)
		}
		fmt.Fprintln(stdout, "Nothing was changed. A down migration can drop data; take a backup, stop the panel, then add --yes.")
		return 2
	}
	timeout, _, err := migrationOptions()
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	done, err := sharedb.Down(ctx, db, fsys, root, n, timeout)
	for _, v := range done {
		fmt.Fprintln(stdout, "reverted "+v)
	}
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	return 0
}

func printStatus(w io.Writer, ms []sharedb.Migration, backupAt time.Time, backupFile string, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tSTATE\tAPPLIED AT")
	pending := 0
	for _, m := range ms {
		state, at := "pending", ""
		switch {
		case !m.Known:
			state = "applied, not in this build"
		case m.Applied:
			state = "applied"
		default:
			pending++
		}
		if m.AppliedAt != nil {
			at = m.AppliedAt.UTC().Format("2006-01-02 15:04 MST")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Version, state, at)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d pending.\n", pending)
	if backupAt.IsZero() {
		fmt.Fprintln(w, "No pre-migration backup is recorded.")
	} else {
		fmt.Fprintf(w, "Newest pre-migration backup: %s (%s ago) %s\n", backupAt.UTC().Format("2006-01-02 15:04 MST"), now.Sub(backupAt).Round(time.Minute), backupFile)
	}
}
