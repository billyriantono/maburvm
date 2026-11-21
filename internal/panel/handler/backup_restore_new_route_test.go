package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// Restoring onto a chosen node skips the owner's quota and picks hardware, so
// it is an operator's action. A customer is refused before any of it runs.
func TestRestoreAsNewIsForAdministrators(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-backup-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newAuthRouteTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '')`).Error)

	svc := service.NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := echo.New()
	RegisterBackupRoutes(e, NewBackupHandler(svc), db)

	call := func(role models.UserRole) int {
		token, err := middleware.GenerateAccessToken(authRouteTestUser(t, db, role))
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/"+uuid.NewString()+"/backups/"+uuid.NewString()+"/restore-new",
			strings.NewReader(`{"node_id":"`+uuid.NewString()+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusForbidden, call(models.RoleClient))
	// An administrator gets past the gate, to "no such backup".
	require.Equal(t, http.StatusNotFound, call(models.RoleAdmin))
}
