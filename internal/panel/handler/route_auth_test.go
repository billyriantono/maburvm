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

// newAuthRouteTestDB builds an isolated SQLite database with the minimal schema
// required by RequireAuth (users) plus nodes/vms so the route handlers' first
// business-logic touch resolves without live PostgreSQL.
func newAuthRouteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0, two_factor_backup_codes TEXT, ip_whitelist TEXT,
			created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token TEXT NOT NULL,
			expires_at DATETIME NOT NULL, ip_address TEXT, user_agent TEXT,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY, name TEXT, ip_address TEXT, status TEXT, token TEXT,
			cert_fingerprint TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
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

func authRouteTestUser(t *testing.T, db *gorm.DB, role models.UserRole) *models.User {
	t.Helper()
	u := &models.User{
		ID:           uuid.New(),
		Email:        uuid.New().String() + "@example.com",
		PasswordHash: "x",
		Role:         role,
	}
	require.NoError(t, db.Create(u).Error)
	return u
}

// TestRegisterNodeRoutes_RequireAuthNonNil proves RegisterNodeRoutes passes the
// supplied DB into RequireAuth (not nil). An authenticated request reaches the
// handler (200 list), and an unauthenticated request is rejected with 401.
func TestRegisterNodeRoutes_RequireAuthNonNil(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-node-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newAuthRouteTestDB(t)

	user := authRouteTestUser(t, db, models.RoleAdmin)
	nodeRepo := repository.NewNodeRepository(db)
	nodeSvc := service.NewNodeService(nodeRepo, db)
	nodeHandler := NewNodeHandler(nodeSvc)

	e := echo.New()
	RegisterNodeRoutes(e, nodeHandler, db)

	// Authenticated request must reach ListNodes (200) via DB-backed RequireAuth.
	token, err := middleware.GenerateAccessToken(user)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "authenticated request should reach node handler via DB-backed RequireAuth")

	// Unauthenticated request must be rejected with 401.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "missing token must be rejected by RequireAuth")
}

// TestRegisterNetworkRoutes_RequireAuthNonNil proves RegisterNetworkRoutes
// passes the supplied DB into RequireAuth. An authenticated request reaches the
// permission layer / handler, and an unauthenticated request is rejected with 401.
func TestRegisterNetworkRoutes_RequireAuthNonNil(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-network-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newAuthRouteTestDB(t)

	user := authRouteTestUser(t, db, models.RoleAdmin)
	vmRepo := repository.NewVMRepository(db)
	nodeRepo := repository.NewNodeRepository(db)
	networkRepo := repository.NewNetworkRepository(db)
	firewallRepo := repository.NewFirewallRepository(db)
	networkSvc := service.NewNetworkService(db, networkRepo, firewallRepo, vmRepo, nodeRepo, nil)
	networkHandler := NewNetworkHandler(networkSvc, authz.NewAuthorizer(db))

	e := echo.New()
	RegisterNetworkRoutes(e, networkHandler, db)

	// Authenticated request hits the route; ListNetworkInterfaces enforces tenant
	// isolation via authz and returns 200/404/403 depending on VM ownership, but it
	// must NOT be stopped by RequireAuth (i.e. not 401).
	token, err := middleware.GenerateAccessToken(user)
	require.NoError(t, err)

	vmID := uuid.New().String()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/vms/"+vmID+"/networks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusUnauthorized, rec.Code, "authenticated request must pass DB-backed RequireAuth")

	// Unauthenticated request must be rejected with 401.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/vms/"+vmID+"/networks", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "missing token must be rejected by RequireAuth")
}

// TestRegisterImportRoutes_RequireAuthNonNil proves RegisterImportRoutes now
// receives and wires the DB into RequireAuth (previously nil). Authenticated
// requests reach the permission layer / handler, and an unauthenticated request
// is rejected with 401. The online check is skipped in tests because no agent
// exists; the auth middleware is the asserted subject here.
func TestRegisterImportRoutes_RequireAuthNonNil(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-import-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newAuthRouteTestDB(t)

	user := authRouteTestUser(t, db, models.RoleAdmin)
	vmRepo := repository.NewVMRepository(db)
	nodeRepo := repository.NewNodeRepository(db)
	templateRepo := repository.NewTemplateRepository(db)
	networkRepo := repository.NewNetworkRepository(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	importSvc := service.NewImportService(db, vmRepo, nodeRepo, templateRepo, networkRepo, nil, logger)
	importHandler := NewImportHandler(importSvc)

	e := echo.New()
	// DB wiring is the Gate 1 remediation: previously RegisterImportRoutes was
	// called with no DB and used RequireAuth(nil). Now it receives db.
	RegisterImportRoutes(e, importHandler, db)

	// Authenticated request reaches the route (PreviewVirtualizor calls into the
	// agent which is unavailable in tests, but auth must not block it -> not 401).
	token, err := middleware.GenerateAccessToken(user)
	require.NoError(t, err)

	nodeID := uuid.New().String()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID+"/import/virtualizor/preview", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusUnauthorized, rec.Code, "authenticated request must pass DB-backed RequireAuth")

	// Unauthenticated request must be rejected with 401.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+nodeID+"/import/virtualizor/preview", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusUnauthorized, rec2.Code, "missing token must be rejected by RequireAuth")
}
