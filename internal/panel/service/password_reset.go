package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ErrInvalidResetToken is returned when a reset token is unknown, expired, or
// already used. It is deliberately generic so callers cannot distinguish these
// cases (which would leak whether a token ever existed).
var ErrInvalidResetToken = errors.New("invalid or expired reset token")

// resetTokenTTL is how long a password-reset link stays valid.
const resetTokenTTL = time.Hour

// hashResetToken returns the SHA-256 hex of a raw reset token. Only the hash is
// persisted, so a database leak does not expose usable reset links.
func hashResetToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RequestPasswordReset issues a reset token for the account with the given email
// and emails the reset link. It is intentionally NON-ENUMERATING: it returns nil
// whether or not the email maps to a real account, and never reveals send
// failures to the caller. The raw token is only ever delivered by email.
//
// resetURLBase is the frontend reset page (e.g. https://panel/reset-password);
// the token is appended as ?token=<raw>.
func (s *UserService) RequestPasswordReset(ctx context.Context, email, resetURLBase string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil
	}

	var user models.User
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no such account — stay silent
		}
		return err // genuine DB error
	}

	// Generate a 256-bit raw token; store only its hash.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	raw := hex.EncodeToString(buf)

	repo := repository.NewEnrollmentRepository(s.db)
	tok := &models.PasswordResetToken{
		UserID:    user.ID,
		TokenHash: hashResetToken(raw),
		ExpiresAt: time.Now().Add(resetTokenTTL),
	}
	if err := repo.CreatePasswordResetToken(ctx, tok); err != nil {
		return err
	}

	// Best-effort email — a send failure must not reveal that the account exists
	// (nor should it fail the request differently from an unknown email).
	if cfg, ok, cerr := LoadSMTPSettings(s.db); cerr == nil && ok {
		resetURL := resetURLBase
		sep := "?"
		if strings.Contains(resetURLBase, "?") {
			sep = "&"
		}
		resetURL = resetURLBase + sep + "token=" + raw
		_ = SendPasswordResetEmail(cfg, user.Email, user.Name, resetURL)
	}
	return nil
}

// ResetPassword consumes a raw reset token and sets a new password. The token is
// single-use (atomically consumed) and the new password must pass the same
// strength policy as registration. On success, all of the user's existing
// sessions are revoked so a stolen session can't outlive the reset.
func (s *UserService) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return ErrInvalidResetToken
	}
	if err := s.validatePasswordStrength(newPassword); err != nil {
		return err
	}

	repo := repository.NewEnrollmentRepository(s.db)
	tok, err := repo.ConsumeResetToken(ctx, hashResetToken(rawToken), time.Now())
	if err != nil {
		// Unknown, expired, or already-used → single generic error.
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrResetNotConsumable) {
			return ErrInvalidResetToken
		}
		return err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), BcryptCost)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Model(&models.User{}).
		Where("id = ?", tok.UserID).
		Update("password_hash", string(hashed)).Error; err != nil {
		return err
	}

	// Invalidate any outstanding sessions for this account.
	return s.RevokeUserTokens(tok.UserID)
}
