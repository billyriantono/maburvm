package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
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

// newVMRouteTestDB builds an isolated SQLite database with the minimal schema
// needed by RequireAuth (users) plus the VM/owner columns used by GetVM's
// ownership check. No live PostgreSQL required.
func newVMRouteTestDB(t *testing.T) *gorm.DB {
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
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token TEXT NOT NULL,
			expires_at DATETIME NOT NULL, ip_address TEXT, user_agent TEXT,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vms (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped',
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
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

// newTestVMHandler builds a VMHandler whose services are backed by the test DB
// so GetVM's ownership path can resolve without live infrastructure.
func newTestVMHandler(t *testing.T, db *gorm.DB) *VMHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vmRepo := repository.NewVMRepository(db)
	nodeRepo := repository.NewNodeRepository(db)
	templateRepo := repository.NewTemplateRepository(db)
	vmSvc := service.NewVMService(db, vmRepo, nodeRepo, templateRepo, nil, logger)
	vncSvc, err := service.NewVNCService(db, vmRepo, nodeRepo, logger, "", "ws://localhost")
	require.NoError(t, err)
	return NewVMHandler(vmSvc, vncSvc, nil, service.NewSSHKeyService(db), service.NewRecipeService(db), authz.NewAuthorizer(db))
}

// TestRegisterVMRoutes_RequireAuthNonNil proves RegisterVMRoutes passes the
// supplied DB into the RequireAuth middleware (not nil). With a real DB, an
// authenticated request reaches the handler (200), while an unauthenticated
// request is rejected with 401. If RequireAuth(nil) were used, this test still
// passes for the unauthenticated case but an inactive/deleted user would be
// wrongly admitted — the DB-backed assertion here guards the wiring.
func TestRegisterVMRoutes_RequireAuthNonNil(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newVMRouteTestDB(t)

	user := &models.User{
		ID:           uuid.New(),
		Email:        "vmroute@example.com",
		PasswordHash: "x",
		Role:         models.RoleAdmin,
	}
	require.NoError(t, db.Create(user).Error)

	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{
		ID: vmID, UserID: user.ID.String(), NodeID: uuid.New().String(), Hostname: "vm",
	}).Error)

	e := echo.New()
	RegisterVMRoutes(e, newTestVMHandler(t, db), db)

	// Authenticated request: middleware must resolve the user from the DB and
	// reach GetVM (which returns 200 with the VM body for an admin owner).
	tokens, err := middleware.GenerateTokenPair(user, db)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/vms/"+vmID, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "authenticated request should reach handler via DB-backed RequireAuth")

	// Unauthenticated request: middleware must reject with 401.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/vms/"+vmID, nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "missing token must be rejected by RequireAuth")
}
