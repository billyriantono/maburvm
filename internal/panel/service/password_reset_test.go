package service

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// TestPasswordResetFlow drives request → emailed link → reset → login, plus the
// token-reuse, expiry, weak-password and unknown-email paths.
func TestPasswordResetFlow(t *testing.T) {
	db := setupTestDB(t)
	if err := db.Exec(`CREATE TABLE password_reset_tokens (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
		expires_at DATETIME NOT NULL, consumed_at DATETIME,
		attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	svc, err := NewUserService(db, generateTestAESKey(), "test-jwt-secret", "MaburVM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(&RegisterRequest{Email: "reset@test.com", Password: "OldP@ssw0rd!1", Role: "client"}); err != nil {
		t.Fatal(err)
	}

	var links []string
	orig := deliverResetLink
	deliverResetLink = func(_ *gorm.DB, to, _, resetURL string) { links = append(links, resetURL) }
	t.Cleanup(func() { deliverResetLink = orig })

	ctx := context.Background()
	request := func() string {
		t.Helper()
		links = nil
		if err := svc.RequestPasswordReset(ctx, " RESET@test.com ", "https://panel/reset?x=1"); err != nil {
			t.Fatalf("RequestPasswordReset: %v", err)
		}
		if len(links) != 1 {
			t.Fatalf("want 1 link, got %v", links)
		}
		u, err := url.Parse(links[0])
		if err != nil || u.Query().Get("x") != "1" || u.Query().Get("token") == "" {
			t.Fatalf("bad reset link %q", links[0])
		}
		return u.Query().Get("token")
	}

	// Unknown email: silent success, nothing sent.
	links = nil
	if err := svc.RequestPasswordReset(ctx, "nobody@test.com", "https://panel/reset"); err != nil || len(links) != 0 {
		t.Fatalf("unknown email: err=%v links=%v", err, links)
	}

	raw := request()
	var stored models.PasswordResetToken
	if err := db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash != hashResetToken(raw) || stored.TokenHash == raw {
		t.Fatal("only the token hash may be persisted")
	}

	// Weak password is rejected before the token is consumed.
	if err := svc.ResetPassword(ctx, raw, "weak"); err == nil {
		t.Fatal("weak password accepted")
	}

	if err := svc.ResetPassword(ctx, raw, "NewP@ssw0rd!2"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, err := svc.Login(&LoginRequest{Email: "reset@test.com", Password: "OldP@ssw0rd!1"}); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := svc.Login(&LoginRequest{Email: "reset@test.com", Password: "NewP@ssw0rd!2"}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	var user models.User
	if err := db.Where("email = ?", "reset@test.com").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.TokenRevokedAt == nil {
		t.Fatal("sessions not revoked after reset")
	}

	// Single use.
	if err := svc.ResetPassword(ctx, raw, "Another@Pass3"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("reused token: %v", err)
	}

	// Expired.
	raw2 := request()
	db.Model(&models.PasswordResetToken{}).Where("token_hash = ?", hashResetToken(raw2)).
		Update("expires_at", time.Now().Add(-time.Minute))
	if err := svc.ResetPassword(ctx, raw2, "Another@Pass3"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("expired token: %v", err)
	}

	// Unknown / empty.
	for _, bad := range []string{"deadbeef", "  "} {
		if err := svc.ResetPassword(ctx, bad, "Another@Pass3"); !errors.Is(err, ErrInvalidResetToken) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}
}
