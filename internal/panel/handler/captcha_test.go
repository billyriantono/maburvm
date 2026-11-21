package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/service"
)

// The sign-in captcha is asked for only after enough failed attempts, is
// checked with the provider, and stays out of the way when it is not set up.
func TestSignInCaptchaAfterFailures(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-captcha-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db, err := gorm.Open(sqlite.Open("file:captcha?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'client', quota_mode TEXT NOT NULL DEFAULT 'legacy', two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0,
			two_factor_backup_codes TEXT, ip_whitelist TEXT, created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME,
			expires_at DATETIME, suspended_at DATETIME, failed_login_count INTEGER NOT NULL DEFAULT 0, locked_until DATETIME,
			totp_last_step INTEGER NOT NULL DEFAULT 0, staff_role TEXT)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`,
		`CREATE TABLE user_sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, method TEXT NOT NULL, ip_address TEXT, user_agent TEXT,
			impersonator_id TEXT, created_at DATETIME, last_seen_at DATETIME, expires_at DATETIME, revoked_at DATETIME)`,
		`CREATE TABLE sign_in_events (id TEXT PRIMARY KEY, user_id TEXT, email TEXT, method TEXT, success BOOLEAN, note TEXT,
			ip_address TEXT, user_agent TEXT, created_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("right-password-1A!"), bcrypt.MinCost)
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, password_hash, role) VALUES ('6f1c1d1e-0000-4000-8000-000000000001', 'c@example.com', ?, 'client')`, string(hash)).Error)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ok := r.PostForm.Get("secret") == "the-secret" && r.PostForm.Get("response") == "passed"
		_ = json.NewEncoder(w).Encode(map[string]any{"success": ok})
	}))
	defer provider.Close()
	saved := captchaVerifyURL["turnstile"]
	captchaVerifyURL["turnstile"] = provider.URL
	defer func() { captchaVerifyURL["turnstile"] = saved }()

	users, err := service.NewUserService(db, "0123456789abcdef0123456789abcdef", "", "test")
	require.NoError(t, err)
	h := NewAuthHandler(users)
	e := echo.New()
	e.POST("/login", h.Login)
	login := func(password, token string) (int, map[string]string) {
		body := `{"email":"c@example.com","password":"` + password + `","captchaToken":"` + token + `"}`
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		out := map[string]string{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Not set up: never asked, however many failures.
	for i := 0; i < 3; i++ {
		code, _ := login("wrong", "")
		assert.Equal(t, http.StatusUnauthorized, code)
	}

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', ?)`,
		`{"captchaProvider":"turnstile","captchaSiteKey":"site-key","captchaSecret":"the-secret","captchaAfterFailures":5}`).Error)
	code, _ := login("wrong", "")
	assert.Equal(t, http.StatusUnauthorized, code, "3 failures so far, the limit is 5")
	code, _ = login("wrong", "")
	assert.Equal(t, http.StatusUnauthorized, code)

	// Five failures from here: the captcha now stands before the password.
	code, body := login("right-password-1A!", "")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "captcha_required", body["code"])
	assert.Equal(t, "turnstile", body["provider"])
	assert.Equal(t, "site-key", body["site_key"])
	code, body = login("right-password-1A!", "forged")
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "captcha_failed", body["code"])
	code, _ = login("right-password-1A!", "passed")
	assert.Equal(t, http.StatusOK, code)

	var failed, succeeded int64
	db.Table("sign_in_events").Where("success = ?", false).Count(&failed)
	db.Table("sign_in_events").Where("success = ?", true).Count(&succeeded)
	assert.Equal(t, int64(5), failed, "refused before the password, a captcha miss is not a failed sign-in")
	assert.Equal(t, int64(1), succeeded)
}
