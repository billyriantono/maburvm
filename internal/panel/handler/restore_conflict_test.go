package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

func newBackupTestHandler(t *testing.T, db *gorm.DB) *BackupHandler {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '', encryption_key_id TEXT NOT NULL DEFAULT '')`).Error)
	svc := service.NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return NewBackupHandler(svc)
}

// A restore of a running VM is refused up front, as a conflict saying why:
// it used to be accepted (backup) or answered 500 (snapshot), and the job
// then cancelled with nobody told.
func TestRestoreOfARunningVMIsAConflict(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", vmID).Update("status", models.VMStatusRunning).Error)
	snapID, backupID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)
	bh := newBackupTestHandler(t, db)
	require.NoError(t, db.Create(&models.Backup{ID: backupID, VMID: vmID, StorageProvider: "s3", StoragePath: "b/x.qcow2",
		BackupType: models.BackupTypeManual, Status: models.BackupStatusCompleted, Checksum: "c", Mode: models.BackupModeFull}).Error)

	for name, call := range map[string]func() int{
		"backup": func() int {
			c, rec := snapshotCtxWithUser(t, admin, models.RoleAdmin)
			c.SetParamNames("id", "backup_id")
			c.SetParamValues(vmID, backupID)
			require.NoError(t, bh.RestoreBackup(c))
			assert.Contains(t, rec.Body.String(), "vm_not_stopped")
			return rec.Code
		},
		"snapshot": func() int {
			c, rec := snapshotCtxWithUser(t, admin, models.RoleAdmin)
			c.SetParamNames("id", "snapshot_id")
			c.SetParamValues(vmID, snapID)
			require.NoError(t, newTestSnapshotHandler(t, db).RestoreSnapshot(c))
			assert.Contains(t, rec.Body.String(), "stop the VM")
			return rec.Code
		},
	} {
		assert.Equal(t, http.StatusConflict, call(), name)
	}
}

// One backup reads as it does in the list: how it was taken, full or
// incremental, and no made-up start time for a backup that has none.
func TestGetBackupSaysWhatTheListSays(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))
	h := newBackupTestHandler(t, db)
	done := time.Now().UTC().Truncate(time.Second)
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.Backup{ID: id, VMID: vmID, StorageProvider: "s3", StoragePath: "b/x.qcow2",
		BackupType: models.BackupTypeManual, Status: models.BackupStatusCompleted, Consistency: models.BackupConsistencyFrozen,
		Mode: models.BackupModeFull, CompletedAt: &done}).Error)

	c, rec := snapshotCtxWithUser(t, admin, models.RoleAdmin)
	c.SetParamNames("id", "backup_id")
	c.SetParamValues(vmID, id)
	require.NoError(t, h.GetBackup(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, models.BackupConsistencyFrozen, body.Data["consistency"])
	assert.Equal(t, models.BackupModeFull, body.Data["mode"])
	assert.NotContains(t, body.Data, "started_at", "not recorded, so not sent as year 1")
	assert.Equal(t, done.Format(time.RFC3339), body.Data["completed_at"])
}
