package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestServer_ManagedNetworksClientDenied verifies the standalone managed-network
// GET is admin-only: a RoleClient is rejected with 403 (topology not leaked),
// even though a client holds network:read. No live DB needed — the role decision
// precedes any repository call.
func TestServer_ManagedNetworksClientDenied(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	s := NewServer(db, &config.Config{Server: config.ServerConfig{AllowedOrigins: "http://localhost:3000"}})
	repo := repository.NewManagedNetworkRepository(db)

	clientCtx, rec := newCtxWithRole(t, models.RoleClient)
	require.NoError(t, s.handleListManagedNetworks(repo)(clientCtx))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "only administrators may list managed networks")
}

// TestServer_ManagedNetworksAdminAllowed verifies an admin passes the role gate.
// The repository call may error on the un-schematized SQLite DB, but it must NOT
// be the 403 policy rejection (the role decision succeeded).
func TestServer_ManagedNetworksAdminAllowed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	s := NewServer(db, &config.Config{Server: config.ServerConfig{AllowedOrigins: "http://localhost:3000"}})
	repo := repository.NewManagedNetworkRepository(db)

	adminCtx, _ := newCtxWithRole(t, models.RoleAdmin)
	require.NoError(t, s.handleListManagedNetworks(repo)(adminCtx))
	// Admin is admitted past the role gate; only the (irrelevant) DB read may fail.
}

func newCtxWithRole(t *testing.T, role models.UserRole) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/networks", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.New(), Role: role})
	return c, rec
}
