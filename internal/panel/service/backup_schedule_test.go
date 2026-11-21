package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSchedulerTestService(t *testing.T) (*BackupService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:backupsched-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE backup_schedules (
			id TEXT PRIMARY KEY, vm_id TEXT, schedule TEXT, status TEXT,
			storage_provider TEXT, compression TEXT, retention_policy TEXT,
			next_run_at DATETIME, last_run_at DATETIME, last_backup_id TEXT,
			full_every_days INTEGER DEFAULT 0, chain_reset_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, deleted_at DATETIME)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	s := NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	return s, db
}

func addSchedule(t *testing.T, db *gorm.DB, id, expr string, next *time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&models.BackupSchedule{ID: id, VMID: "vm-" + id, Schedule: expr,
		Status: models.BackupScheduleStatusActive, StorageProvider: "s3", NextRunAt: next}).Error)
}

func nextRunOf(t *testing.T, db *gorm.DB, id string) time.Time {
	t.Helper()
	var sc models.BackupSchedule
	require.NoError(t, db.First(&sc, "id = ?", id).Error)
	require.NotNil(t, sc.NextRunAt)
	return *sc.NextRunAt
}

func TestScheduleBackupSavesTheNextRun(t *testing.T) {
	s, db := newSchedulerTestService(t)
	addSchedule(t, db, "a", "0 2 * * *", nil)
	sc := &models.BackupSchedule{ID: "a", VMID: "vm-a", Schedule: "0 2 * * *"}
	before := time.Now()
	require.NoError(t, s.ScheduleBackup(context.Background(), sc))
	next := nextRunOf(t, db, "a")
	require.True(t, next.After(before) && next.Sub(before) <= 24*time.Hour, "next run %s", next)
	require.Equal(t, 2, next.In(time.Local).Hour())

	require.ErrorIs(t, s.ScheduleBackup(context.Background(), &models.BackupSchedule{ID: "a", Schedule: "not a cron expr"}), ErrInvalidCronExpression)
}

// Runs missed while the panel was down are caught up by one backup, and a
// second pass (or a second panel) does not start it again.
func TestRunDueCatchesUpAMissedRunOnce(t *testing.T) {
	s, db := newSchedulerTestService(t)
	var fired []string
	s.fire = func(_ context.Context, sc *models.BackupSchedule, _ time.Time) { fired = append(fired, sc.ID) }

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
	threeDaysAgo := now.Add(-72 * time.Hour)
	tomorrow := now.Add(24 * time.Hour)
	addSchedule(t, db, "missed", "0 2 * * *", &threeDaysAgo)
	addSchedule(t, db, "later", "0 2 * * *", &tomorrow)
	addSchedule(t, db, "new", "0 2 * * *", nil)

	s.RunDue(context.Background(), now)
	require.Equal(t, []string{"missed"}, fired, "only the overdue schedule runs, once")
	require.Equal(t, time.Date(2026, 11, 20, 2, 0, 0, 0, time.Local), nextRunOf(t, db, "missed").In(time.Local),
		"the next run is the next one from now, not the next missed one")
	require.Equal(t, time.Date(2026, 11, 20, 2, 0, 0, 0, time.Local), nextRunOf(t, db, "new").In(time.Local),
		"a schedule that never ran is scheduled from now, not run")

	s.RunDue(context.Background(), now.Add(time.Minute))
	require.Len(t, fired, 1, "a run is started once")

	// A claim that lost the race does not fire.
	ok, err := s.scheduleRepo.ClaimRun(context.Background(), "missed", now, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, ok, "a schedule no longer due cannot be claimed again")
}

func TestRunDueWaitsForTheBackupWindow(t *testing.T) {
	s, db := newSchedulerTestService(t)
	var fired int
	s.fire = func(context.Context, *models.BackupSchedule, time.Time) { fired++ }
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES (?, ?)`,
		queue.BackupPolicySection, `{"window_start":"22:00","window_end":"06:00"}`).Error)

	noon := time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
	due := noon.Add(-time.Hour)
	addSchedule(t, db, "a", "0 11 * * *", &due)

	s.RunDue(context.Background(), noon)
	require.Zero(t, fired, "nothing starts outside the window")
	require.Equal(t, due.Unix(), nextRunOf(t, db, "a").Unix(), "the schedule stays due")

	s.RunDue(context.Background(), time.Date(2026, 11, 19, 22, 1, 0, 0, time.Local))
	require.Equal(t, 1, fired, "it runs when the window opens")
}

func TestBackupsToPrune(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	// One backup a day at 03:00 for 40 days, plus a second one today.
	var backups []models.Backup
	for d := 0; d < 40; d++ {
		at := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC).AddDate(0, 0, -d)
		backups = append(backups, models.Backup{ID: fmt.Sprintf("d%02d", d), CreatedAt: at})
	}
	backups = append(backups, models.Backup{ID: "today-2", CreatedAt: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)})
	kept := func(p models.BackupRetentionPolicy) map[string]bool {
		out := map[string]bool{}
		for _, b := range backups {
			out[b.ID] = true
		}
		for _, b := range backupsToPrune(backups, p, now) {
			delete(out, b.ID)
		}
		return out
	}

	require.Len(t, kept(models.BackupRetentionPolicy{}), len(backups), "no policy keeps everything")

	k := kept(models.BackupRetentionPolicy{KeepLast: 3})
	require.Equal(t, map[string]bool{"today-2": true, "d00": true, "d01": true}, k)

	// The newest of each of the last 7 days, the newest of 4 weeks: 7 daily
	// plus older weekly ones, never both of today's.
	k = kept(models.BackupRetentionPolicy{KeepDaily: 7, KeepWeekly: 4})
	require.True(t, k["today-2"] && !k["d00"], "one per day: today's newest")
	for d := 1; d <= 6; d++ {
		require.True(t, k[fmt.Sprintf("d%02d", d)], "day %d", d)
	}
	require.False(t, k["d07"] && k["d08"], "days past a week are kept one a week at most")
	require.LessOrEqual(t, len(k), 7+4)

	k = kept(models.BackupRetentionPolicy{MaxAgeDays: 10})
	require.True(t, k["d09"] && !k["d11"] && !k["d39"], "age alone removes only what is too old")

	k = kept(models.BackupRetentionPolicy{KeepLast: 30, MaxAgeDays: 5})
	require.True(t, k["d04"] && !k["d06"], "age wins over count")

	old := []models.Backup{{ID: "only", CreatedAt: now.AddDate(-1, 0, 0)}}
	require.Empty(t, backupsToPrune(old, models.BackupRetentionPolicy{MaxAgeDays: 1}, now), "the newest backup is always kept")
}

func TestValidRetention(t *testing.T) {
	require.NoError(t, validRetention(models.BackupRetentionPolicy{KeepLast: 7, KeepWeekly: 4, MaxAgeDays: 90}))
	require.ErrorIs(t, validRetention(models.BackupRetentionPolicy{KeepLast: -1}), ErrInvalidRetention)
	require.ErrorIs(t, validRetention(models.BackupRetentionPolicy{MaxAgeDays: 99999}), ErrInvalidRetention)
	require.True(t, validCompression("zstd") && validCompression("") && !validCompression("bzip2"))
}
