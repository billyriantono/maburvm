package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/maburvm/panel/internal/shared/config"
)

const migrationsDir = "internal/shared/db/migrations"

func main() {
	if len(os.Args) > 1 && os.Args[1] != "up" {
		log.Fatalf("unsupported command %q; only 'up' is supported", os.Args[1])
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	dsn := databaseURL(cfg.Database)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	if err := ensureSchemaMigrations(ctx, db); err != nil {
		log.Fatalf("failed to ensure schema_migrations: %v", err)
	}

	files, err := migrationFiles(migrationsDir)
	if err != nil {
		log.Fatalf("failed to list migrations: %v", err)
	}
	if len(files) == 0 {
		log.Printf("no migrations found in %s", migrationsDir)
		return
	}

	for _, file := range files {
		version := strings.TrimSuffix(filepath.Base(file), ".up.sql")
		applied, err := isApplied(ctx, db, version)
		if err != nil {
			log.Fatalf("failed checking migration %s: %v", version, err)
		}
		if applied {
			log.Printf("skip %s", version)
			continue
		}
		if err := applyMigration(ctx, db, version, file); err != nil {
			log.Fatalf("failed applying %s: %v", version, err)
		}
		log.Printf("applied %s", version)
	}
}

// databaseURL delegates to the shared, URL-encoded config helper so the panel,
// server migrations/River, and this command all build the DSN identically and
// escape special characters in credentials safely.
func databaseURL(cfg config.DatabaseConfig) string {
	return cfg.DatabaseURL()
}

func ensureSchemaMigrations(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func isApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var existing string
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = $1`, version).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func applyMigration(ctx context.Context, db *sql.DB, version, file string) error {
	contents, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
		return err
	}
	return tx.Commit()
}
