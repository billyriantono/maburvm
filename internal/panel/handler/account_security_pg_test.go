//go:build pg

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// Lockout, single-use codes and their audit rows on the real schema: the audit
// row's address is an inet and its actor a uuid, which SQLite does not check.
func TestPG_AccountSecurityOnTheRealSchema(t *testing.T) {
	db := migratedPG(t)
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	ctx := context.Background()
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', '{"lockoutAttempts": 3, "lockoutMinutes": 5}'::jsonb)
		ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data`).Error)
	svc, err := service.NewUserService(db, "0123456789abcdef0123456789abcdef", "test-jwt-secret", "MaburVM")
	require.NoError(t, err)
	const password = "StrongP@ssw0rd!"
	reg, err := svc.Register(&service.RegisterRequest{Email: "pg-lock@example.com", Password: password, Role: "client"})
	require.NoError(t, err)
	user := reg.User

	e := echo.New()
	e.POST("/login", NewAuthHandler(svc).Login)
	login := func(pw, code string) (int, http.Header, map[string]any) {
		body, _ := json.Marshal(map[string]string{"email": user.Email, "password": pw, "totpCode": code})
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.9:4000"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, rec.Header(), out
	}

	for i := 0; i < 3; i++ {
		code, _, _ := login("wrong", "")
		require.Equal(t, http.StatusUnauthorized, code)
	}
	code, header, out := login(password, "")
	require.Equal(t, http.StatusTooManyRequests, code, out)
	require.Equal(t, "account_locked", out["code"])
	require.NotEmpty(t, header.Get("Retry-After"))

	var audit models.AuditLog
	require.NoError(t, db.Where("action = ? AND resource_id = ?", "user.locked", user.ID.String()).First(&audit).Error)
	require.Equal(t, "198.51.100.9", audit.IPAddress)
	require.Nil(t, audit.UserID, "the panel locked it, not a person")

	require.NoError(t, service.ClearSignInLock(ctx, db, user.ID))
	code, _, out = login(password, "")
	require.Equal(t, http.StatusOK, code, out)

	// An authenticator code is taken once.
	setup, err := svc.Setup2FA(&service.Setup2FARequest{UserID: user.ID})
	require.NoError(t, err)
	now, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, svc.Verify2FASetup(&service.Verify2FASetupRequest{UserID: user.ID, TOTPCode: now}))
	code, _, _ = login(password, now)
	require.Equal(t, http.StatusUnauthorized, code, "the code that turned two-factor on is spent")
	next, err := totp.GenerateCode(setup.Secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	code, _, out = login(password, next)
	require.Equal(t, http.StatusOK, code, out)

	// An administrator's temporary password ends every session and lifts a lock.
	require.NoError(t, db.Exec(`UPDATE users SET locked_until = NOW() + interval '1 hour' WHERE id = ?`, user.ID).Error)
	temp, err := svc.SetTemporaryPassword(ctx, user.ID)
	require.NoError(t, err)
	var after models.User
	require.NoError(t, db.First(&after, "id = ?", user.ID).Error)
	require.False(t, after.SignInLocked())
	require.NotNil(t, after.TokenRevokedAt)
	service.AuditUserEvent(ctx, db, "user.password_reset", user.ID.String(), user.ID.String(), "", "", map[string]any{"method": "temporary"})
	var resets int64
	require.NoError(t, db.Model(&models.AuditLog{}).Where("action = ?", "user.password_reset").Count(&resets).Error)
	require.Equal(t, int64(1), resets, "an audit row without an address is still written")
	code, _, out = login(temp, setup.BackupCodes[0])
	require.Equal(t, http.StatusOK, code, out)
}
