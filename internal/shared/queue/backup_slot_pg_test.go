//go:build pg

package queue

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sharedb "github.com/maburvm/panel/internal/shared/db"
)

// slotTestDB is a throwaway database with every migration applied.
func slotTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	name := fmt.Sprintf("maburvm_bkslot_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '` + name + `'`)
		_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `"`)
	})
	dsn := strings.Replace(base, "/postgres?", "/"+name+"?", 1)
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	entries, err := fs.ReadDir(sharedb.MigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := fs.ReadFile(sharedb.MigrationsFS, path.Join("migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB(); err == nil {
			s.Close()
		}
	})
	return db
}

// Many workers racing for one node's last slot: exactly one gets it.
func TestClaimBackupSlotIsOnePerSlotOnPostgres(t *testing.T) {
	db := slotTestDB(t)
	const node = "33333333-3333-3333-3333-333333333333"
	seed := []string{
		`INSERT INTO users (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111', 'alice@example.com', 'x')`,
		`INSERT INTO nodes (id, name, ip_address, token) VALUES ('` + node + `', 'n1', '192.0.2.1', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('44444444-4444-4444-4444-444444444444', 'debian', '12', '/x', 'c')`,
	}
	var backups []string
	for i := 0; i < 8; i++ {
		vm := fmt.Sprintf("55555555-5555-5555-5555-%012d", i)
		bk := fmt.Sprintf("66666666-6666-6666-6666-%012d", i)
		backups = append(backups, bk)
		seed = append(seed,
			`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('`+vm+`', '11111111-1111-1111-1111-111111111111', '`+node+`', 'web-`+fmt.Sprint(i)+`', '44444444-4444-4444-4444-444444444444')`,
			`INSERT INTO backups (id, vm_id, storage_provider, storage_path, status) VALUES ('`+bk+`', '`+vm+`', 's3', 'k`+fmt.Sprint(i)+`', 'pending')`)
	}
	for _, s := range seed {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for _, bk := range backups {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ok, err := claimBackupSlot(context.Background(), db, id, node, 1, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(bk)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d backups took the node's single slot", won)
	}
	var running int64
	db.Table("backups").Where("status = 'in_progress'").Count(&running)
	if running != 1 {
		t.Fatalf("%d backups marked in progress", running)
	}
}
