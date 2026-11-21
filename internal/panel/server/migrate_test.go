package server

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMigrationOptions(t *testing.T) {
	t.Setenv("MIGRATION_TIMEOUT", "")
	t.Setenv("MIGRATION_REQUIRE_BACKUP_WITHIN", "")
	timeout, req, err := migrationOptions()
	if err != nil || timeout != defaultMigrationTimeout || req != 0 {
		t.Fatalf("defaults: %v %v %v", timeout, req, err)
	}

	t.Setenv("MIGRATION_TIMEOUT", "45m")
	t.Setenv("MIGRATION_REQUIRE_BACKUP_WITHIN", "6h")
	timeout, req, err = migrationOptions()
	if err != nil || timeout != 45*time.Minute || req != 6*time.Hour {
		t.Fatalf("set: %v %v %v", timeout, req, err)
	}

	t.Setenv("MIGRATION_REQUIRE_BACKUP_WITHIN", "0")
	if _, req, _ = migrationOptions(); req != 0 {
		t.Fatalf("0 turns the requirement off, got %v", req)
	}

	for _, bad := range []string{"30", "-1m", "soon"} {
		t.Setenv("MIGRATION_TIMEOUT", bad)
		if _, _, err := migrationOptions(); err == nil {
			t.Fatalf("MIGRATION_TIMEOUT=%q must stop the boot", bad)
		}
	}
}

func TestBackupFresh(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := backupFresh(time.Time{}, "", time.Hour, now); err == nil || !strings.Contains(err.Error(), "none is recorded") {
		t.Fatalf("no marker: %v", err)
	}
	if err := backupFresh(now.Add(-2*time.Hour), "backups/x.dump", time.Hour, now); err == nil || !strings.Contains(err.Error(), "2h0m0s old") {
		t.Fatalf("stale marker: %v", err)
	}
	if err := backupFresh(now.Add(-10*time.Minute), "backups/x.dump", time.Hour, now); err != nil {
		t.Fatalf("fresh marker refused: %v", err)
	}
}

func TestMigrateCommandRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{{"down"}, {"down", "0"}, {"down", "two"}, {"status", "extra"}, {"sideways"}} {
		var out, errOut bytes.Buffer
		if code := MigrateCommand(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut.String())
		}
	}
	var out bytes.Buffer
	if code := MigrateCommand([]string{"help"}, &out, &out); code != 0 || !strings.Contains(out.String(), "down N") {
		t.Fatalf("help: %d %q", code, out.String())
	}
}
