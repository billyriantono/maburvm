package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// A suspended account's open session and its API keys stop working at once,
// and work again once the suspension is lifted.
func TestSuspendedAccountSessionsAndAPIKeysStop(t *testing.T) {
	db := setupAuthTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN DEFAULT 1,
		role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0, scope TEXT NOT NULL DEFAULT '', allowed_ips TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	t.Cleanup(func() { db.Exec(`DROP TABLE api_keys`) })

	user := &models.User{Email: "suspend-me@example.com", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(user).Error)
	const apiToken = "mvk_suspended_account_test_token"
	require.NoError(t, db.Create(&models.APIKey{UserID: user.ID.String(), Name: "ci", KeyHash: models.HashAPIToken(apiToken), Prefix: "mvk_susp", IsActive: true}).Error)
	session, err := GenerateAccessToken(user)
	require.NoError(t, err)

	call := func(header, value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)
		req.Header.Set(header, value)
		rec := httptest.NewRecorder()
		h := RequireAuth(db)(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		require.NoError(t, h(echo.New().NewContext(req, rec)))
		return rec
	}
	bySession := func() *httptest.ResponseRecorder { return call("Authorization", "Bearer "+session) }
	byKey := func() *httptest.ResponseRecorder { return call("X-API-Key", apiToken) }
	setSuspended := func(at interface{}) {
		require.NoError(t, db.Table("users").Where("id = ?", user.ID.String()).Update("suspended_at", at).Error)
	}

	assert.Equal(t, http.StatusOK, bySession().Code)
	assert.Equal(t, http.StatusOK, byKey().Code)

	setSuspended(time.Now())
	rec := bySession()
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "account_suspended")
	assert.Equal(t, http.StatusUnauthorized, byKey().Code)

	setSuspended(nil)
	assert.Equal(t, http.StatusOK, bySession().Code)
	assert.Equal(t, http.StatusOK, byKey().Code)
}
