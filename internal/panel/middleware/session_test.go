package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessTokenCookieName {
			return c
		}
	}
	return nil
}

func TestSessionCookieIsHttpOnlyAndSecureOverHTTPS(t *testing.T) {
	for proto, secure := range map[string]bool{"https": true, "https,http": true, "http": false, "": false} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		if proto != "" {
			req.Header.Set(echo.HeaderXForwardedProto, proto)
		}
		rec := httptest.NewRecorder()
		SetSessionCookie(echo.New().NewContext(req, rec), "tok", 8*time.Hour)
		ck := sessionCookie(rec)
		require.NotNil(t, ck)
		assert.True(t, ck.HttpOnly, proto)
		assert.Equal(t, http.SameSiteLaxMode, ck.SameSite, proto)
		assert.Equal(t, secure, ck.Secure, proto)
		assert.Equal(t, 8*3600, ck.MaxAge, proto)
	}
}

func TestSessionCookieAuthGuardsCrossSiteWritesAndDropsDeadSessions(t *testing.T) {
	db := setupAuthTestDB(t)
	user := &models.User{Email: "cookie@example.com", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(user).Error)
	token, err := GenerateAccessToken(user)
	require.NoError(t, err)

	call := func(method, cookie, bearer, site string, downstream int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/vms", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: cookie})
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h := RequireAuth(db)(func(c echo.Context) error { return c.NoContent(downstream) })
		require.NoError(t, h(echo.New().NewContext(req, rec)))
		return rec
	}

	assert.Equal(t, http.StatusOK, call(http.MethodGet, token, "", "", 200).Code, "the browser's cookie signs the request in")
	assert.Equal(t, http.StatusOK, call(http.MethodPost, token, "", "same-origin", 200).Code)
	assert.Equal(t, http.StatusOK, call(http.MethodGet, token, "", "cross-site", 200).Code, "a cross-site link may still open a page")
	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, token, "", "cross-site", 200).Code, "a cross-site form may not change anything")
	assert.Equal(t, http.StatusForbidden, call(http.MethodDelete, token, "", "same-site", 200).Code, "nor may a sibling subdomain, which SameSite=Lax lets through")
	assert.Equal(t, http.StatusOK, call(http.MethodPost, token, "", "none", 200).Code)
	assert.Equal(t, http.StatusOK, call(http.MethodPost, "", token, "cross-site", 200).Code, "a Bearer header is never sent on its own")

	// A session the panel refuses is removed from the browser...
	rec := call(http.MethodGet, "not-a-token", "", "", 200)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	ck := sessionCookie(rec)
	require.NotNil(t, ck, "the dead cookie is cleared")
	assert.Less(t, ck.MaxAge, 0)
	// ...but a handler's own 401 about something else does not sign anyone out.
	rec = call(http.MethodGet, token, "", "", http.StatusUnauthorized)
	assert.Nil(t, sessionCookie(rec))
}

// Sign-out must drop the HttpOnly cookie even when the panel refuses the
// session with a 403 (an IP allow-list), or the browser can never reach the
// sign-in page again: the web app cannot remove the cookie itself.
func TestSignOutDropsCookieWhenSessionRefused(t *testing.T) {
	db := setupAuthTestDB(t)
	user := &models.User{Email: "moved@example.com", PasswordHash: "x", Role: models.RoleClient, IPWhitelist: []string{"203.0.113.5"}}
	require.NoError(t, db.Create(user).Error)
	token, err := GenerateAccessToken(user)
	require.NoError(t, err)

	logout := func(site string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		req.RemoteAddr = "198.51.100.9:4321"
		req.AddCookie(&http.Cookie{Name: AccessTokenCookieName, Value: token})
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h := DropSessionCookie(RequireAuth(db)(func(c echo.Context) error { return c.NoContent(http.StatusOK) }))
		require.NoError(t, h(echo.New().NewContext(req, rec)))
		return rec
	}

	rec := logout("same-origin")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	ck := sessionCookie(rec)
	require.NotNil(t, ck, "the refused session is still signed out of the browser")
	assert.Less(t, ck.MaxAge, 0)
	assert.Nil(t, sessionCookie(logout("cross-site")), "another site cannot sign someone out")
}

func TestAPIRateLimitPerUserAndKey(t *testing.T) {
	saved := apiLimits
	apiLimits = newAPILimiter()
	t.Cleanup(func() { apiLimits = saved })

	db := setupAuthTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	t.Cleanup(func() { db.Exec(`DROP TABLE system_settings`) })
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN DEFAULT 1,
		role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0, scope TEXT NOT NULL DEFAULT '', allowed_ips TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	t.Cleanup(func() { db.Exec(`DROP TABLE api_keys`) })
	// 6 a minute: one request at a time, the next a token every 10 seconds.
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', '{"apiRequestsPerMinute":6}')`).Error)

	alice := &models.User{Email: "alice-rl@example.com", PasswordHash: "x", Role: models.RoleClient}
	bob := &models.User{Email: "bob-rl@example.com", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(alice).Error)
	require.NoError(t, db.Create(bob).Error)
	const key = "mvk_rate_limit_test_token"
	require.NoError(t, db.Create(&models.APIKey{UserID: alice.ID.String(), Name: "ci", KeyHash: models.HashAPIToken(key), Prefix: "mvk_rate", IsActive: true}).Error)

	call := func(header, value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)
		req.Header.Set(header, value)
		rec := httptest.NewRecorder()
		h := RequireAuth(db)(RequireAuth(db)(func(c echo.Context) error { return c.NoContent(http.StatusOK) }))
		require.NoError(t, h(echo.New().NewContext(req, rec)))
		return rec
	}
	bearer := func(u *models.User) *httptest.ResponseRecorder {
		tok, err := GenerateAccessToken(u)
		require.NoError(t, err)
		return call("Authorization", "Bearer "+tok)
	}

	assert.Equal(t, http.StatusOK, bearer(alice).Code, "RequireAuth twice on a route counts once")
	rec := bearer(alice)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("Retry-After"))
	assert.True(t, strings.Contains(rec.Body.String(), "rate_limited"))
	assert.Equal(t, http.StatusOK, bearer(bob).Code, "another account has its own allowance")
	assert.Equal(t, http.StatusOK, call("X-API-Key", key).Code, "a key is limited apart from its owner's sessions")
	assert.Equal(t, http.StatusTooManyRequests, call("X-API-Key", key).Code)

	// 0 turns the limit off (read again after the 30 second cache).
	require.NoError(t, db.Exec(`UPDATE system_settings SET data = '{"apiRequestsPerMinute":0}' WHERE section = 'api'`).Error)
	apiLimits.readAt = time.Time{}
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusOK, bearer(alice).Code)
	}
}
