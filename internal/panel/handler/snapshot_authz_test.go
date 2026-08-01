package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newSnapshotAuthzTestDB builds an isolated SQLite DB with the schema used by
// the snapshot handler authz tests (no live PostgreSQL). The authz helpers only
// read vm_id/user_id, so Postgres-specific types are omitted.
func newSnapshotAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					two_factor_secret TEXT, two_factor_backup_codes TEXT, ip_whitelist TEXT,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vms (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped',
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, name TEXT NOT NULL, disk_path TEXT NOT NULL,
			status TEXT DEFAULT 'pending', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	}
	for _, s := range ddl {
		require.NoError(t, db.Exec(s).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

// snapshotCtxWithUser builds an echo context with the authenticated user set
// (as the RequireAuth middleware would), so the authz helpers resolve identity.
func snapshotCtxWithUser(t *testing.T, userID string, role models.UserRole) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(userID), Role: role})
	return c, rec
}

func newTestSnapshotHandler(t *testing.T, db *gorm.DB) *SnapshotHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	snapSvc := service.NewSnapshotService(
		db,
		repository.NewSnapshotRepository(db),
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		nil,
		logger,
	)
	return NewSnapshotHandler(snapSvc, authz.NewAuthorizer(db))
}

// TestSnapshotHandler_OwnerGetSnapshotOK verifies an owner reaches GetSnapshot
// for a snapshot that belongs to their VM.
func TestSnapshotHandler_OwnerGetSnapshotOK(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	snapID := uuid.New().String()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, snapID)

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestSnapshotHandler_AdminGetSnapshotOK verifies an admin can read another
// user's snapshot (legitimate admin support denied by the old owner-only check).
func TestSnapshotHandler_AdminGetSnapshotOK(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)
	snapID := uuid.New().String()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, admin, models.RoleAdmin)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, snapID)

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestSnapshotHandler_OtherUserGetSnapshot404 verifies a non-owner maps to 404.
func TestSnapshotHandler_OtherUserGetSnapshot404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	snapID := uuid.New().String()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, other, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, snapID)

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_NonexistentVMGetSnapshot404 verifies a nonexistent VM maps
// to 404 (identical to a non-owner, anti-enumeration parity).
func TestSnapshotHandler_NonexistentVMGetSnapshot404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(uuid.New().String(), uuid.New().String())

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_SnapshotMismatch404 verifies a snapshot belonging to a
// different VM than the route VM maps to 404 (confused-deputy guard).
func TestSnapshotHandler_SnapshotMismatch404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmA := seedVM(t, db, owner)
	vmB := seedVM(t, db, owner)
	snapB := uuid.New().String()
	// Snapshot belongs to vmB, but the route targets vmA (mismatch).
	require.NoError(t, db.Create(&models.Snapshot{ID: snapB, VMID: vmB, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmA, snapB)

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_UnauthenticatedGetSnapshot401 verifies missing auth maps
// to 401 on GetSnapshot.
func TestSnapshotHandler_UnauthenticatedGetSnapshot401(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))

	h := newTestSnapshotHandler(t, db)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, uuid.New().String())

	require.NoError(t, h.GetSnapshot(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestSnapshotHandler_OwnerListOK verifies an owner reaches ListSnapshots.
func TestSnapshotHandler_OwnerListOK(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.ListSnapshots(c))
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
	assert.NotEqual(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_OtherUserList404 verifies a non-owner maps to 404 on list.
func TestSnapshotHandler_OtherUserList404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, other, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.ListSnapshots(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_OwnerCreateOK verifies an owner reaches CreateSnapshot.
func TestSnapshotHandler_OwnerCreateOK(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"snap"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.SetRequest(req)

	// CreateSnapshot enqueues a River job; with a nil client it panics AFTER the
	// authz boundary is crossed. Recover the panic and assert the owner was NOT
	// rejected at the tenant boundary (not 401/404).
	func() {
		defer func() { _ = recover() }()
		_ = h.CreateSnapshot(c)
	}()
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "owner must not be rejected by authz")
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "owner must not be rejected by authz")
}

// TestSnapshotHandler_OtherUserCreate404 verifies a non-owner maps to 404 on create.
func TestSnapshotHandler_OtherUserCreate404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, other, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"snap"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.SetRequest(req)

	require.NoError(t, h.CreateSnapshot(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSnapshotHandler_OwnerDeleteOK verifies an owner reaches DeleteSnapshot.
func TestSnapshotHandler_OwnerDeleteOK(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	snapID := uuid.New().String()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, owner, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, snapID)

	// DeleteSnapshot enqueues a River job; with a nil client it panics AFTER the
	// authz boundary is crossed. Recover and assert the owner was NOT rejected.
	func() {
		defer func() { _ = recover() }()
		_ = h.DeleteSnapshot(c)
	}()
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "owner must not be rejected by authz")
	assert.NotEqual(t, http.StatusNotFound, rec.Code, "owner must not be rejected by authz")
}

// TestSnapshotHandler_OtherUserDelete404 verifies a non-owner maps to 404 on delete.
func TestSnapshotHandler_OtherUserDelete404(t *testing.T) {
	db := newSnapshotAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	snapID := uuid.New().String()
	require.NoError(t, db.Create(&models.Snapshot{ID: snapID, VMID: vmID, Name: "snap", DiskPath: "/x", Status: models.SnapshotStatusComplete}).Error)

	h := newTestSnapshotHandler(t, db)
	c, rec := snapshotCtxWithUser(t, other, models.RoleClient)
	c.SetParamNames("id", "snapshot_id")
	c.SetParamValues(vmID, snapID)

	require.NoError(t, h.DeleteSnapshot(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
