package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// newConsoleRevokeTestDB builds an isolated SQLite DB with the minimal schema for
// the console-token revoke route tests (no live PostgreSQL).
func newConsoleRevokeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					two_factor_secret TEXT, two_factor_backup_codes TEXT, ip_whitelist TEXT,
			created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vms (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped',
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS console_tokens (
			id TEXT PRIMARY KEY, jti TEXT NOT NULL, vm_id TEXT NOT NULL, user_id TEXT NOT NULL,
			token TEXT NOT NULL, expires_at DATETIME NOT NULL, revoked BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME)`,
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

func newTestVMHandlerWithAuthz(t *testing.T, db *gorm.DB) *VMHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vmRepo := repository.NewVMRepository(db)
	nodeRepo := repository.NewNodeRepository(db)
	vncSvc, err := service.NewVNCService(db, vmRepo, nodeRepo, logger, "", "ws://localhost")
	require.NoError(t, err)
	return NewVMHandler(nil, vncSvc, nil, service.NewSSHKeyService(db), service.NewRecipeService(db), authz.NewAuthorizer(db))
}

func revokeBody(jti string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"jti":"`+jti+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

func seedConsoleToken(t *testing.T, db *gorm.DB, vmID, owner, jti string) {
	t.Helper()
	require.NoError(t, db.Create(&service.ConsoleToken{
		ID:        uuid.New().String(),
		JTI:       jti,
		VMID:      vmID,
		UserID:    owner,
		Token:     "tok-" + jti,
		ExpiresAt: time.Now().Add(time.Hour),
	}).Error)
}

func tokenRevoked(t *testing.T, db *gorm.DB, jti string) bool {
	t.Helper()
	var tok service.ConsoleToken
	require.NoError(t, db.Where("jti = ?", jti).First(&tok).Error)
	return tok.Revoked
}

// TestRevokeConsoleToken_OwnerRevokesOwnToken verifies an owner can revoke a
// token that belongs to their VM.
func TestRevokeConsoleToken_OwnerRevokesOwnToken(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	jti := "jti-owner"
	seedConsoleToken(t, db, vmID, owner, jti)

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody(jti)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(owner), Role: models.RoleClient})
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, tokenRevoked(t, db, jti), "token should be marked revoked")
}

// TestRevokeConsoleToken_AdminRevokesOtherVMToken verifies an admin can revoke a
// token on another user's VM (legitimate admin support).
func TestRevokeConsoleToken_AdminRevokesOtherVMToken(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)
	jti := "jti-admin"
	seedConsoleToken(t, db, vmID, owner, jti)

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody(jti)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(admin), Role: models.RoleAdmin})
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, tokenRevoked(t, db, jti), "token should be marked revoked")
}

// TestRevokeConsoleToken_OtherUserGets404 verifies a non-owner attempting to
// revoke a token on a VM they don't control is rejected (authorized at the
// route VM level) with 404 (anti-enumeration), and the token is NOT revoked.
func TestRevokeConsoleToken_OtherUserGets404(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	jti := "jti-other"
	seedConsoleToken(t, db, vmID, owner, jti)

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody(jti)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(other), Role: models.RoleClient})
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, tokenRevoked(t, db, jti), "token must not be revoked by non-owner")
}

// TestRevokeConsoleToken_JTIBelongsToOtherVM404 verifies that supplying a JTI
// that belongs to a different tenant's VM returns 404 (cannot revoke a token
// that isn't on the authorized route VM) and does NOT revoke it.
func TestRevokeConsoleToken_JTIBelongsToOtherVM404(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	ownerA := seedUser(t, db, models.RoleClient)
	ownerB := seedUser(t, db, models.RoleClient)
	vmA := seedVM(t, db, ownerA)
	vmB := seedVM(t, db, ownerB)
	// JTI belongs to vmB, but the caller authorizes/revokes against vmA.
	jti := "jti-crossvm"
	seedConsoleToken(t, db, vmB, ownerB, jti)

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody(jti)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Caller is the owner of vmA and authorized for vmA.
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(ownerA), Role: models.RoleClient})
	c.SetParamNames("id")
	c.SetParamValues(vmA)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// The cross-VM token must remain unrevoked (not leaked / not revoked).
	var tok service.ConsoleToken
	require.NoError(t, db.Where("jti = ?", jti).First(&tok).Error)
	assert.False(t, tok.Revoked, "cross-VM token must not be revoked")
	assert.Equal(t, vmB, tok.VMID)
}

// TestRevokeConsoleToken_UnknownJTI404 verifies an unknown JTI maps to 404 and
// does not leak existence.
func TestRevokeConsoleToken_UnknownJTI404(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody("does-not-exist")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(owner), Role: models.RoleClient})
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestRevokeConsoleToken_Unauthenticated401 verifies missing auth maps to 401.
func TestRevokeConsoleToken_Unauthenticated401(t *testing.T) {
	db := newConsoleRevokeTestDB(t)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))

	h := newTestVMHandlerWithAuthz(t, db)
	e := echo.New()
	req := revokeBody("whatever")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.RevokeConsoleToken(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
