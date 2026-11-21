package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// With "require two-factor for staff" on, an administrator without it reaches
// only the pages that set it up; customers and administrators with it are not
// affected, and nothing changes while the setting is off.
func TestStaffWithoutTwoFactorAreHeldToSetup(t *testing.T) {
	db := setupAuthTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	t.Cleanup(func() { db.Exec(`DROP TABLE system_settings`) })

	newUser := func(email string, role models.UserRole, twoFactor bool) string {
		u := &models.User{Email: email, PasswordHash: "x", Role: role, TwoFactorEnabled: twoFactor}
		require.NoError(t, db.Create(u).Error)
		token, err := GenerateAccessToken(u)
		require.NoError(t, err)
		return token
	}
	admin := newUser("staff-no-2fa@example.com", models.RoleAdmin, false)
	admin2FA := newUser("staff-2fa@example.com", models.RoleAdmin, true)
	client := newUser("customer-no-2fa@example.com", models.RoleClient, false)

	call := func(token, path string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		var seen *UserContext
		h := RequireAuth(db)(func(c echo.Context) error {
			seen, _ = GetUserContext(c)
			return c.NoContent(http.StatusOK)
		})
		require.NoError(t, h(echo.New().NewContext(req, rec)))
		body := map[string]interface{}{}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if seen != nil {
			body["setup_required"] = seen.TwoFactorSetupRequired
		}
		return rec.Code, body
	}

	code, _ := call(admin, "/api/v1/vms")
	assert.Equal(t, http.StatusOK, code, "off by default")

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', '{"requireStaff2fa": true}')`).Error)

	code, body := call(admin, "/api/v1/vms")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "two_factor_required", body["code"])
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/auth/logout", "/api/v1/settings/2fa/setup", "/api/v1/settings/2fa/verify", "/api/v1/settings/profile"} {
		code, body = call(admin, path)
		assert.Equal(t, http.StatusOK, code, path)
		assert.Equal(t, true, body["setup_required"], path)
	}
	code, _ = call(admin, "/api/v1/settings/system")
	assert.Equal(t, http.StatusForbidden, code, "the rest of settings stays closed")
	code, _ = call(admin, "/api/v1/settings/profile/../system")
	assert.Equal(t, http.StatusForbidden, code)

	code, body = call(admin2FA, "/api/v1/vms")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, body["setup_required"])
	code, _ = call(client, "/api/v1/vms")
	assert.Equal(t, http.StatusOK, code, "customers are not held to it")
}
