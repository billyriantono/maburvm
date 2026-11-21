//go:build pg

package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func throwawayDB(t *testing.T) *sql.DB {
	t.Helper()
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	name := fmt.Sprintf("maburvm_migrate_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		a, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
	})
	return db
}

func TestUpStatusDown(t *testing.T) {
	ctx := context.Background()
	db := throwawayDB(t)
	fsys := fstest.MapFS{
		"m/001_a.up.sql":   {Data: []byte(`CREATE TABLE a (id int)`)},
		"m/001_a.down.sql": {Data: []byte(`DROP TABLE a`)},
		"m/002_b.up.sql":   {Data: []byte(`ALTER TABLE a ADD COLUMN b int`)},
		"m/002_b.down.sql": {Data: []byte(`ALTER TABLE a DROP COLUMN b`)},
		"m/003_c.up.sql":   {Data: []byte(`ALTER TABLE a ADD COLUMN c int`)},
	}
	if err := EnsureMigrationsTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	var seen []string
	n, err := Up(ctx, db, fsys, "m", time.Minute, func(v string, _ time.Duration) { seen = append(seen, v) })
	if err != nil || n != 3 || strings.Join(seen, ",") != "001_a,002_b,003_c" {
		t.Fatalf("up: n=%d seen=%v err=%v", n, seen, err)
	}
	if n, err := Up(ctx, db, fsys, "m", time.Minute, nil); err != nil || n != 0 {
		t.Fatalf("second up applied %d, err %v", n, err)
	}

	if _, err := DownPlan(ctx, db, fsys, "m", 1); err == nil || !strings.Contains(err.Error(), "no down migration") {
		t.Fatalf("003 has no down file, got %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = '003_c'; ALTER TABLE a DROP COLUMN c`); err != nil {
		t.Fatal(err)
	}
	reverted, err := Down(ctx, db, fsys, "m", 1, time.Minute)
	if err != nil || strings.Join(reverted, ",") != "002_b" {
		t.Fatalf("down: %v %v", reverted, err)
	}
	if _, err := db.Exec(`SELECT b FROM a`); err == nil {
		t.Fatal("column b should be gone")
	}
	ms, err := Status(ctx, db, fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	if p := Pending(ms); strings.Join(p, ",") != "002_b,003_c" {
		t.Fatalf("pending after down: %v", p)
	}
	if !ms[0].Applied || !ms[0].HasDown || ms[2].HasDown {
		t.Fatalf("status: %+v", ms)
	}
	if _, err := DownPlan(ctx, db, fsys, "m", 5); err == nil {
		t.Fatal("reverting more than is applied must be refused")
	}

	// A version from a newer panel is listed, and never reverted by this build.
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES ('999_future', NOW() + interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	ms, _ = Status(ctx, db, fsys, "m")
	if last := ms[len(ms)-1]; last.Version != "999_future" || last.Known || !last.Applied {
		t.Fatalf("unknown version: %+v", last)
	}
	if _, err := DownPlan(ctx, db, fsys, "m", 1); err == nil || !strings.Contains(err.Error(), "newer panel") {
		t.Fatalf("got %v", err)
	}
}

func TestUpTimesOutPerMigrationAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db := throwawayDB(t)
	fsys := fstest.MapFS{
		"m/001_fast.up.sql": {Data: []byte(`CREATE TABLE fast (id int)`)},
		"m/002_slow.up.sql": {Data: []byte(`CREATE TABLE slow (id int); SELECT pg_sleep(5)`)},
	}
	if err := EnsureMigrationsTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	n, err := Up(ctx, db, fsys, "m", 500*time.Millisecond, nil)
	if n != 1 || err == nil || !strings.Contains(err.Error(), "MIGRATION_TIMEOUT") {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var slow sql.NullString
	_ = db.QueryRow(`SELECT to_regclass('slow')::text`).Scan(&slow)
	if slow.Valid {
		t.Fatal("the timed-out migration must leave nothing behind")
	}
	ms, _ := Status(ctx, db, fsys, "m")
	if p := Pending(ms); len(p) != 1 || p[0] != "002_slow" {
		t.Fatalf("pending: %v", p)
	}
}
