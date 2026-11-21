package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// A customer manages their own API keys from a session, and the list says
// when the operator has turned API access off, so the page can say why keys fail.
func TestCustomerAPIKeysAndTheAccessSwitch(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newVMRouteTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY, user_id TEXT, name TEXT, key_hash TEXT, prefix TEXT,
		last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN, role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)

	customer := &models.User{ID: uuid.New(), Email: "keys@example.com", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(customer).Error)
	session, err := middleware.GenerateAccessToken(customer)
	require.NoError(t, err)

	e := echo.New()
	RegisterAPIKeyRoutes(e, NewAPIKeyHandler(service.NewAPIKeyService(db)), db)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+session)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := call(http.MethodPost, "/api/v1/api-keys", `{"name":"backup script"}`)
	require.Equal(t, http.StatusCreated, code, out)
	created := out["data"].(map[string]any)
	require.True(t, strings.HasPrefix(created["token"].(string), models.APITokenPrefix), "the token is shown once, at creation")

	code, out = call(http.MethodGet, "/api/v1/api-keys", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["api_enabled"])
	keys := out["data"].([]any)
	require.Len(t, keys, 1)
	require.NotContains(t, keys[0].(map[string]any), "token", "a listed key never carries the token")

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', '{"enableApi":false}')`).Error)
	_, out = call(http.MethodGet, "/api/v1/api-keys", "")
	require.Equal(t, false, out["api_enabled"])

	code, _ = call(http.MethodDelete, "/api/v1/api-keys/"+created["id"].(string), "")
	require.Equal(t, http.StatusOK, code)
	_, out = call(http.MethodGet, "/api/v1/api-keys", "")
	require.Empty(t, out["data"])
}
