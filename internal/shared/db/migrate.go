package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// Migration is one schema version and whether the database has it.
type Migration struct {
	Version   string     `json:"version"`
	Applied   bool       `json:"applied"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
	// Known is false for a version the database has and this build does not:
	// the database was migrated by a newer panel.
	Known   bool `json:"known"`
	HasDown bool `json:"has_down"`
}

// EnsureMigrationsTable creates schema_migrations if it is missing.
func EnsureMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
		)`)
	return err
}

// Versions lists the up migrations under root, in the order they apply.
func Versions(fsys fs.FS, root string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			out = append(out, strings.TrimSuffix(e.Name(), ".up.sql"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Status lists every migration this build has, then any applied version it
// does not know.
func Status(ctx context.Context, db *sql.DB, fsys fs.FS, root string) ([]Migration, error) {
	versions, err := Versions(fsys, root)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[string]time.Time{}
	for rows.Next() {
		var v string
		var at time.Time
		if err := rows.Scan(&v, &at); err != nil {
			return nil, err
		}
		applied[v] = at
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Migration, 0, len(versions))
	known := map[string]bool{}
	for _, v := range versions {
		known[v] = true
		m := Migration{Version: v, Known: true, HasDown: exists(fsys, path.Join(root, v+".down.sql"))}
		if at, ok := applied[v]; ok {
			m.Applied, m.AppliedAt = true, &at
		}
		out = append(out, m)
	}
	var unknown []string
	for v := range applied {
		if !known[v] {
			unknown = append(unknown, v)
		}
	}
	sort.Strings(unknown)
	for _, v := range unknown {
		at := applied[v]
		out = append(out, Migration{Version: v, Applied: true, AppliedAt: &at})
	}
	return out, nil
}

// Pending returns the known, unapplied versions in apply order.
func Pending(ms []Migration) []string {
	var out []string
	for _, m := range ms {
		if m.Known && !m.Applied {
			out = append(out, m.Version)
		}
	}
	return out
}

// Up applies every pending migration in order, each in its own transaction and
// under its own timeout. done is told about each one applied.
func Up(ctx context.Context, db *sql.DB, fsys fs.FS, root string, timeout time.Duration, done func(version string, took time.Duration)) (int, error) {
	ms, err := Status(ctx, db, fsys, root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range Pending(ms) {
		body, err := fs.ReadFile(fsys, path.Join(root, v+".up.sql"))
		if err != nil {
			return n, err
		}
		start := time.Now()
		if err := step(ctx, db, timeout, string(body), `INSERT INTO schema_migrations(version) VALUES ($1)`, v); err != nil {
			return n, fmt.Errorf("migration %s failed: %w", v, err)
		}
		n++
		if done != nil {
			done(v, time.Since(start))
		}
	}
	return n, nil
}

// DownPlan returns the versions Down(n) would revert, newest first, or why it
// cannot: every one must be known to this build and have a .down.sql.
func DownPlan(ctx context.Context, db *sql.DB, fsys fs.FS, root string, n int) ([]string, error) {
	if n < 1 {
		return nil, errors.New("the number of migrations to revert must be 1 or more")
	}
	// Applied order, not version order: a letter-suffixed migration (033a)
	// added later is applied after higher numbers and must be undone first.
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY applied_at DESC, version DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		plan = append(plan, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(plan) < n {
		return nil, fmt.Errorf("only %d migrations are applied, so %d cannot be reverted", len(plan), n)
	}
	for _, v := range plan {
		if !exists(fsys, path.Join(root, v+".up.sql")) {
			return nil, fmt.Errorf("%s was applied by a newer panel; revert it with that version's migrate command", v)
		}
		if !exists(fsys, path.Join(root, v+".down.sql")) {
			return nil, fmt.Errorf("%s has no down migration", v)
		}
	}
	return plan, nil
}

// Down reverts the n most recently applied migrations, newest first, each in
// its own transaction. It stops at the first failure.
func Down(ctx context.Context, db *sql.DB, fsys fs.FS, root string, n int, timeout time.Duration) ([]string, error) {
	plan, err := DownPlan(ctx, db, fsys, root, n)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, v := range plan {
		body, err := fs.ReadFile(fsys, path.Join(root, v+".down.sql"))
		if err != nil {
			return done, err
		}
		if err := step(ctx, db, timeout, string(body), `DELETE FROM schema_migrations WHERE version = $1`, v); err != nil {
			return done, fmt.Errorf("reverting %s failed: %w", v, err)
		}
		done = append(done, v)
	}
	return done, nil
}

// step runs one migration file and its bookkeeping statement in one transaction.
func step(ctx context.Context, db *sql.DB, timeout time.Duration, body, record, version string) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if _, err := tx.ExecContext(ctx, body); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("did not finish within %s and was rolled back (raise MIGRATION_TIMEOUT): %w", timeout, err)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, record, version); err != nil {
		return err
	}
	return tx.Commit()
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil
}
