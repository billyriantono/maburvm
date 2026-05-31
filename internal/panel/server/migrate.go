package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	sharedb "github.com/maburvm/panel/internal/shared/db"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// runMigrations applies all pending SQL migrations on startup. Migrations are
// embedded in the binary (so they apply regardless of the working directory);
// set MIGRATIONS_DIR to apply on-disk migrations instead (e.g. new files
// without a rebuild).
func runMigrations(dsn string) error {
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

	// Ensure schema_migrations table exists
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return err
	}

	fsys, root := migrationsSource()
	files, err := migrationFiles(fsys, root)
	if err != nil {
		// Do NOT skip silently: a missing migration source is an operational error
		// that would otherwise leave the schema half-built.
		return fmt.Errorf("failed to read migrations from %q: %w", root, err)
	}
	if len(files) == 0 {
		slog.Default().Warn("no migrations found", "root", root)
		return nil
	}

	applied := 0
	for _, file := range files {
		version := strings.TrimSuffix(path.Base(file), ".up.sql")

		var existing string
		err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = $1`, version).Scan(&existing)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		contents, err := fs.ReadFile(fsys, file)
		if err != nil {
			return err
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s failed: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.Default().Info("applied migration", "version", version)
		applied++
	}

	slog.Default().Info("migrations up to date", "applied_now", applied, "total", len(files))
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

func migrationFiles(fsys fs.FS, root string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		files = append(files, path.Join(root, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}
