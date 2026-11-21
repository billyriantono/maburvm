package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// Limits and throttles are the operator's: a customer may not lift or set
// them on their own VM, and a bad value is refused before anything is stored.
func TestVMLimitsRoutesAreStaffOnly(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db, err := gorm.Open(sqlite.Open("file:vmlimits?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
			quota_mode TEXT NOT NULL DEFAULT 'legacy', two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0,
			two_factor_backup_codes TEXT, ip_whitelist TEXT, created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT, os_template_id TEXT, resources TEXT,
			status TEXT DEFAULT 'stopped', suspended BOOLEAN NOT NULL DEFAULT 0, source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0, limits TEXT NOT NULL DEFAULT '{}', throttle TEXT, limits_note TEXT,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(s).Error)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			sqlDB.Close()
		}
	})

	client := &models.User{ID: uuid.New(), Email: "owner@example.com", PasswordHash: "x", Role: models.RoleClient}
	admin := &models.User{ID: uuid.New(), Email: "staff@example.com", PasswordHash: "x", Role: models.RoleAdmin}
	require.NoError(t, db.Create(client).Error)
	require.NoError(t, db.Create(admin).Error)
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: client.ID.String(), NodeID: uuid.New().String(), Hostname: "vm"}).Error)

	e := echo.New()
	RegisterVMRoutes(e, newTestVMHandler(t, db), db)
	call := func(u *models.User, method, path, body string) int {
		token, err := middleware.GenerateAccessToken(u)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "/api/v1/vms/"+vmID+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusForbidden, call(client, http.MethodPut, "/limits", `{"limits":{}}`))
	require.Equal(t, http.StatusForbidden, call(client, http.MethodDelete, "/throttle", ``))
	require.Equal(t, http.StatusForbidden, call(client, http.MethodPost, "/limits/apply", ``))

	require.Equal(t, http.StatusBadRequest, call(admin, http.MethodPut, "/limits", `{}`), "limits is required")
	require.Equal(t, http.StatusBadRequest, call(admin, http.MethodPut, "/limits", `{"limits":{"cpu_cap_percent":150}}`))
	require.Equal(t, http.StatusBadRequest, call(admin, http.MethodPost, "/throttle", `{}`), "a throttle that caps nothing")
	var stored string
	require.NoError(t, db.Raw(`SELECT limits FROM vms WHERE id = ?`, vmID).Scan(&stored).Error)
	require.Equal(t, "{}", stored, "nothing stored for refused limits")

	// Stored even though the node is unknown here; the answer says it is not applied.
	require.Equal(t, http.StatusOK, call(admin, http.MethodPut, "/limits", `{"limits":{"cpu_cap_percent":50}}`))
	require.NoError(t, db.Raw(`SELECT limits FROM vms WHERE id = ?`, vmID).Scan(&stored).Error)
	require.Contains(t, stored, `"cpu_cap_percent":50`)
	var note *string
	require.NoError(t, db.Raw(`SELECT limits_note FROM vms WHERE id = ?`, vmID).Scan(&note).Error)
	require.NotNil(t, note, "the VM says its node does not have the limits")
}
