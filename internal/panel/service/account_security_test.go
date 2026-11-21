package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const securityTestPassword = "StrongP@ssw0rd!"

// securityTestService is a user service over the users table plus the
// settings and audit tables the sign-in rules read and write.
func securityTestService(t *testing.T, securitySettings string) (*UserService, *gorm.DB, *models.User) {
	t.Helper()
	db := setupTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT, resource_id TEXT,
		ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`).Error)
	if securitySettings != "" {
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', ?)`, securitySettings).Error)
	}
	svc, err := NewUserService(db, generateTestAESKey(), "test-jwt-secret", "MaburVM")
	require.NoError(t, err)
	resp, err := svc.Register(&RegisterRequest{Email: "locked@test.com", Password: securityTestPassword, Role: "client"})
	require.NoError(t, err)
	return svc, db, resp.User
}

func signIn(svc *UserService, password, code string) (*LoginResponse, error) {
	return svc.Login(&LoginRequest{Email: "locked@test.com", Password: password, TOTPCode: code, ClientIP: "198.51.100.7"})
}

func TestSignInPolicyDefaultsAndLimits(t *testing.T) {
	_, db, _ := securityTestService(t, "")
	save := func(data string) {
		require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', ?)`, data).Error)
	}
	assert.Equal(t, middleware.SignInPolicy{LockoutAttempts: 10, LockoutMinutes: 15, SessionHours: 24, CaptchaAfterFailures: 3}, middleware.LoadSignInPolicy(db))
	assert.Equal(t, middleware.DefaultSignInPolicy(), middleware.LoadSignInPolicy(nil))

	// What the Security tab saved before any of it was enforced is not picked
	// up now: an old "5 attempts" or "60 minute session" never applied.
	save(`{"minPasswordLength": 12, "sessionTimeout": 60, "maxLoginAttempts": 5, "lockoutDuration": 30}`)
	assert.Equal(t, middleware.DefaultSignInPolicy(), middleware.LoadSignInPolicy(db))

	save(`{"lockoutAttempts": 0, "lockoutMinutes": 5, "requireStaff2fa": true, "sessionHours": 72}`)
	p := middleware.LoadSignInPolicy(db)
	assert.Equal(t, middleware.SignInPolicy{LockoutAttempts: 0, LockoutMinutes: 5, RequireStaff2FA: true, SessionHours: 72, CaptchaAfterFailures: 3}, p)
	assert.Equal(t, 72*time.Hour, p.SessionLifetime())

	save(`{"lockoutAttempts": 1, "lockoutMinutes": 0, "sessionHours": 100000}`)
	assert.Equal(t, middleware.SignInPolicy{LockoutAttempts: 3, LockoutMinutes: 1, SessionHours: 720, CaptchaAfterFailures: 3}, middleware.LoadSignInPolicy(db))
}

func TestAccountLocksAfterFailedSignIns(t *testing.T) {
	svc, db, user := securityTestService(t, `{"lockoutAttempts": 3, "lockoutMinutes": 10}`)
	reload := func() models.User {
		var u models.User
		require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
		return u
	}

	// A sign-in between wrong guesses starts the count again.
	_, err := signIn(svc, "wrong", "")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = signIn(svc, "wrong", "")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Equal(t, 2, reload().FailedLoginCount)
	_, err = signIn(svc, securityTestPassword, "")
	require.NoError(t, err)
	assert.Equal(t, 0, reload().FailedLoginCount)

	for i := 0; i < 3; i++ {
		_, err = signIn(svc, "wrong", "")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	}
	locked := reload()
	require.True(t, locked.SignInLocked(), "three wrong passwords in a row lock the account")
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), *locked.LockedUntil, time.Minute)

	// Locked: the right password is refused too, and says for how long.
	_, err = signIn(svc, securityTestPassword, "")
	var lockedErr *AccountLockedError
	require.ErrorAs(t, err, &lockedErr)
	assert.Equal(t, "10 minutes", lockedErr.Wait())

	var audited int64
	require.NoError(t, db.Table("audit_logs").Where("action = ? AND resource_id = ?", "user.locked", user.ID.String()).Count(&audited).Error)
	assert.Equal(t, int64(1), audited)

	// It unlocks by itself, with a clean count.
	require.NoError(t, db.Exec("UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(-time.Second), user.ID).Error)
	_, err = signIn(svc, "wrong", "")
	assert.ErrorIs(t, err, ErrInvalidCredentials, "after the lock ran out a wrong password is just wrong")
	stillLocked := reload()
	assert.False(t, stillLocked.SignInLocked())
	_, err = signIn(svc, securityTestPassword, "")
	assert.NoError(t, err)

	// An administrator's unlock, and a password reset, lift it early.
	require.NoError(t, db.Exec("UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(time.Hour), user.ID).Error)
	require.NoError(t, ClearSignInLock(context.Background(), db, user.ID))
	_, err = signIn(svc, securityTestPassword, "")
	assert.NoError(t, err)
}

// From outside an account's IP allow-list the password is never checked: the
// answer must not tell a right password from a wrong one, and wrong guesses
// from there must not lock the owner out.
func TestOutsideTheAllowListThePasswordIsNotTried(t *testing.T) {
	svc, db, user := securityTestService(t, `{"lockoutAttempts": 3}`)
	require.NoError(t, db.Exec(`UPDATE users SET ip_whitelist = ? WHERE id = ?`, `["192.0.2.0/24"]`, user.ID).Error)
	try := func(password, ip string) error {
		_, err := svc.Login(&LoginRequest{Email: "locked@test.com", Password: password, ClientIP: ip})
		return err
	}
	for i := 0; i < 5; i++ {
		assert.ErrorIs(t, try("wrong", "198.51.100.7"), ErrIPNotWhitelisted)
	}
	assert.ErrorIs(t, try(securityTestPassword, "198.51.100.7"), ErrIPNotWhitelisted, "same answer for the right password")
	require.NoError(t, try(securityTestPassword, "192.0.2.10"), "the owner, from an allowed address, is not locked out")

	for i := 0; i < 3; i++ {
		assert.ErrorIs(t, try("wrong", "192.0.2.10"), ErrInvalidCredentials)
	}
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
	assert.True(t, u.SignInLocked(), "wrong guesses from an allowed address still lock")
}

// A backup code read by two requests at once is taken by one of them only.
func TestBackupCodeCannotBeUsedTwiceConcurrently(t *testing.T) {
	svc, db, user := securityTestService(t, "")
	setup, err := svc.Setup2FA(&Setup2FARequest{UserID: user.ID})
	require.NoError(t, err)
	var stale models.User
	require.NoError(t, db.First(&stale, "id = ?", user.ID).Error)
	var first models.User
	require.NoError(t, db.First(&first, "id = ?", user.ID).Error)

	assert.True(t, svc.verifyBackupCode(&first, setup.BackupCodes[0]))
	assert.False(t, svc.verifyBackupCode(&stale, setup.BackupCodes[0]), "the second request read the codes before the first marked it used")
	assert.True(t, svc.verifyBackupCode(&first, setup.BackupCodes[1]), "the winner's copy stays current")
}

func TestLockoutCanBeTurnedOff(t *testing.T) {
	svc, db, user := securityTestService(t, `{"lockoutAttempts": 0}`)
	for i := 0; i < 4; i++ {
		_, err := signIn(svc, "wrong", "")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	}
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
	assert.False(t, u.SignInLocked())
	assert.Equal(t, 0, u.FailedLoginCount)
}

func TestAuthenticatorCodeWorksOnce(t *testing.T) {
	svc, db, user := securityTestService(t, `{"lockoutAttempts": 3}`)
	setup, err := svc.Setup2FA(&Setup2FARequest{UserID: user.ID})
	require.NoError(t, err)
	now := time.Now()
	code := func(stepsAhead int) string {
		c, err := totp.GenerateCode(setup.Secret, now.Add(time.Duration(stepsAhead)*totpStepSeconds*time.Second))
		require.NoError(t, err)
		return c
	}
	require.NoError(t, svc.Verify2FASetup(&Verify2FASetupRequest{UserID: user.ID, TOTPCode: code(0)}))

	// The code that turned 2FA on is spent, and so is anything older.
	_, err = signIn(svc, securityTestPassword, code(0))
	assert.ErrorIs(t, err, ErrInvalidTOTPCode, "a code already accepted")
	_, err = signIn(svc, securityTestPassword, code(-1))
	assert.ErrorIs(t, err, ErrInvalidTOTPCode, "a code older than the last one accepted")

	// The password step alone does not wipe those two wrong codes.
	resp, err := signIn(svc, securityTestPassword, "")
	require.NoError(t, err)
	assert.True(t, resp.Requires2FA)
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
	assert.Equal(t, 2, u.FailedLoginCount)

	resp, err = signIn(svc, securityTestPassword, code(1))
	require.NoError(t, err, "the next code signs in")
	assert.NotEmpty(t, resp.Token)
	_, err = signIn(svc, securityTestPassword, code(1))
	assert.ErrorIs(t, err, ErrInvalidTOTPCode, "and only once")

	// Wrong codes from a held session lock the account like wrong sign-ins do,
	// and a locked account takes no more guesses, right or wrong.
	for i := 0; i < 2; i++ {
		assert.ErrorIs(t, svc.Verify2FA(&Verify2FARequest{UserID: user.ID, Code: "000000"}), ErrInvalidTOTPCode)
	}
	var lockedErr *AccountLockedError
	assert.ErrorAs(t, svc.Verify2FA(&Verify2FARequest{UserID: user.ID, Code: setup.BackupCodes[0]}), &lockedErr)

	// A new secret starts its own record: turning 2FA back on at once works.
	require.NoError(t, ClearSignInLock(context.Background(), db, user.ID))
	require.NoError(t, svc.Disable2FA(user.ID))
	setup, err = svc.Setup2FA(&Setup2FARequest{UserID: user.ID})
	require.NoError(t, err)
	assert.NoError(t, svc.Verify2FASetup(&Verify2FASetupRequest{UserID: user.ID, TOTPCode: code(0)}))
}

func TestBackupCodeWorksOnceInEitherCase(t *testing.T) {
	svc, _, user := securityTestService(t, "")
	setup, err := svc.Setup2FA(&Setup2FARequest{UserID: user.ID})
	require.NoError(t, err)
	c, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, svc.Verify2FASetup(&Verify2FASetupRequest{UserID: user.ID, TOTPCode: c}))

	backup := setup.BackupCodes[0]
	resp, err := signIn(svc, securityTestPassword, strings.ToLower(backup))
	require.NoError(t, err, "a backup code typed in lowercase")
	assert.NotEmpty(t, resp.Token)
	_, err = signIn(svc, securityTestPassword, backup)
	assert.ErrorIs(t, err, ErrInvalidTOTPCode, "a backup code is spent once used")
}

func TestSessionLengthFollowsTheSetting(t *testing.T) {
	svc, _, _ := securityTestService(t, `{"sessionHours": 72}`)
	assert.Equal(t, 72*time.Hour, svc.SessionLifetime())
	resp, err := signIn(svc, securityTestPassword, "")
	require.NoError(t, err)
	claims, err := middleware.ParseAndValidateToken(resp.Token)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(72*time.Hour), claims.ExpiresAt.Time, time.Minute)
}

func TestTemporaryPassword(t *testing.T) {
	// The strictest policy an operator can set still accepts a generated password.
	strict := PasswordPolicy{MinLength: 128, RequireUppercase: true, RequireNumbers: true, RequireSpecial: true}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := GenerateTemporaryPassword(strict.MinLength)
		require.NoError(t, err)
		require.NoError(t, strict.Check(p), p)
		short, err := GenerateTemporaryPassword(8)
		require.NoError(t, err)
		require.Len(t, short, 16)
		require.NoError(t, PasswordPolicy{MinLength: 16, RequireUppercase: true, RequireNumbers: true, RequireSpecial: true}.Check(short), short)
		require.False(t, seen[short], "passwords must not repeat")
		seen[short] = true
	}

	svc, db, user := securityTestService(t, `{"lockoutAttempts": 3}`)
	require.NoError(t, db.Exec("UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(time.Hour), user.ID).Error)
	temp, err := svc.SetTemporaryPassword(context.Background(), user.ID)
	require.NoError(t, err)

	_, err = signIn(svc, temp, "")
	require.NoError(t, err, "the temporary password signs in, lock lifted")
	_, err = signIn(svc, securityTestPassword, "")
	assert.ErrorIs(t, err, ErrInvalidCredentials, "the old password is gone")
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
	assert.NotNil(t, u.TokenRevokedAt, "sessions from before the reset are ended")

	_, err = svc.SetTemporaryPassword(context.Background(), models.User{}.ID)
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestAdminResetLinkSaysWhenItCannotBeSent(t *testing.T) {
	svc, db, user := securityTestService(t, "")
	require.NoError(t, db.Exec(`CREATE TABLE password_reset_tokens (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
		expires_at DATETIME NOT NULL, consumed_at DATETIME,
		attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`).Error)
	var link string
	orig := sendResetLinkNow
	sendResetLinkNow = func(_ SMTPSettings, _, _, resetURL string) error { link = resetURL; return nil }
	t.Cleanup(func() { sendResetLinkNow = orig })
	ctx := context.Background()

	t.Setenv("PANEL_PUBLIC_URL", "")
	assert.ErrorIs(t, svc.SendPasswordResetLink(ctx, user), ErrPublicURLNotSet)
	t.Setenv("PANEL_PUBLIC_URL", "https://panel.example.com/")
	assert.ErrorIs(t, svc.SendPasswordResetLink(ctx, user), ErrEmailNotConfigured)
	assert.Empty(t, link)

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('email', ?)`, `{"smtpHost":"mail.example.com","smtpPort":587}`).Error)
	require.NoError(t, svc.SendPasswordResetLink(ctx, user))
	u, err := url.Parse(link)
	require.NoError(t, err)
	assert.Equal(t, "https://panel.example.com/reset-password", u.Scheme+"://"+u.Host+u.Path)

	// The link is an ordinary reset link, and using it lifts a sign-in lock.
	require.NoError(t, db.Exec("UPDATE users SET locked_until = ? WHERE id = ?", time.Now().Add(time.Hour), user.ID).Error)
	require.NoError(t, svc.ResetPassword(ctx, u.Query().Get("token"), "An0ther-Passw0rd!"))
	_, err = signIn(svc, "An0ther-Passw0rd!", "")
	assert.NoError(t, err)

	sendResetLinkNow = func(SMTPSettings, string, string, string) error { return errors.New("connection refused") }
	assert.ErrorContains(t, svc.SendPasswordResetLink(ctx, user), "connection refused")
}
