package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSchedulerTestService(t *testing.T) *BackupService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:backupsched-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	// Minimal table for UpdateNextRun (the only DB write ScheduleBackup performs).
	require.NoError(t, db.Exec(`CREATE TABLE backup_schedules (
		id TEXT PRIMARY KEY, vm_id TEXT, schedule TEXT, status TEXT,
		storage_provider TEXT, compression TEXT, retention_policy TEXT,
		next_run_at DATETIME, last_run_at DATETIME, last_backup_id TEXT,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
	)`).Error)

	return NewBackupService(
		db,
		repository.NewBackupRepository(db),
		repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		nil, // riverClient — only used when a cron job fires, not in these tests
		nil, // storageClient
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	)
}

func TestScheduleBackupIsIdempotentPerVM(t *testing.T) {
	s := newSchedulerTestService(t)
	ctx := context.Background()

	require.NoError(t, s.ScheduleBackup(ctx, &models.BackupSchedule{
		ID: "sched-1", VMID: "vm-1", Schedule: "0 0 * * *", Status: models.BackupScheduleStatusActive,
	}))
	require.Len(t, s.cron.Entries(), 1, "one schedule -> one cron entry")

	// Rescheduling the SAME VM must replace, not duplicate, the cron entry —
	// otherwise the old expression keeps firing forever.
	require.NoError(t, s.ScheduleBackup(ctx, &models.BackupSchedule{
		ID: "sched-1b", VMID: "vm-1", Schedule: "30 2 * * *", Status: models.BackupScheduleStatusActive,
	}))
	require.Len(t, s.cron.Entries(), 1, "rescheduling the same VM must not leak a cron entry")

	// A different VM adds a second entry.
	require.NoError(t, s.ScheduleBackup(ctx, &models.BackupSchedule{
		ID: "sched-2", VMID: "vm-2", Schedule: "0 3 * * *", Status: models.BackupScheduleStatusActive,
	}))
	require.Len(t, s.cron.Entries(), 2)

	// Unscheduling removes only that VM's entry.
	s.UnscheduleBackup("vm-1")
	require.Len(t, s.cron.Entries(), 1)
}

func TestScheduleBackupRejectsInvalidCron(t *testing.T) {
	s := newSchedulerTestService(t)
	err := s.ScheduleBackup(context.Background(), &models.BackupSchedule{
		ID: "bad", VMID: "vm-x", Schedule: "not a cron expr", Status: models.BackupScheduleStatusActive,
	})
	require.Error(t, err)
	require.Empty(t, s.cron.Entries(), "an invalid expression must not register an entry")
}
