package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	// ErrEmailNotConfigured means the panel cannot send mail yet.
	ErrEmailNotConfigured = errors.New("email is not set up: add a mail server under Settings, System, Email")
	// ErrPublicURLNotSet means the panel does not know the address to put in a link.
	ErrPublicURLNotSet = errors.New("the panel does not know its public address (System settings, Panel, or PANEL_PUBLIC_URL), so it cannot email a link")
)

// AuditUserEvent records something done to an account: user.locked,
// user.unlock, user.password_reset, user.two_factor_reset, user.update,
// user.delete. actorID is empty when the panel did it by itself. Never
// silent: a lost row is logged.
func AuditUserEvent(ctx context.Context, db *gorm.DB, action, actorID, userID, ip, userAgent string, details map[string]any) {
	entry := &models.AuditLog{
		UserID:       strPtrOrNil(actorID),
		Action:       action,
		ResourceType: "USER",
		ResourceID:   &userID,
		IPAddress:    ip,
		UserAgent:    userAgent,
		Details:      details,
	}
	tx := db.WithContext(ctx)
	if net.ParseIP(ip) == nil {
		tx = tx.Omit("ip_address") // the column is an inet; an empty string is not one
	}
	if err := tx.Create(entry).Error; err != nil {
		slog.Default().Error("audit: could not record account event", "action", action, "account", userID, "actor", actorID, "error", err)
	}
}

// GenerateTemporaryPassword returns a random password that passes any policy
// the operator can set: at least 16 characters with every character class,
// and none of the characters people misread (0/O, 1/l/I).
func GenerateTemporaryPassword(minLength int) (string, error) {
	const (
		lower   = "abcdefghijkmnopqrstuvwxyz"
		upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		digits  = "23456789"
		symbols = "!@#$%*-_=+"
	)
	n := func(limit int) (int, error) {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
		if err != nil {
			return 0, err
		}
		return int(v.Int64()), nil
	}
	length := min(max(minLength, 16), 128)
	out := make([]byte, 0, length)
	for len(out) < length {
		set := lower + upper + digits + symbols
		// The first four positions guarantee one character of each class.
		if len(out) < 4 {
			set = []string{lower, upper, digits, symbols}[len(out)]
		}
		i, err := n(len(set))
		if err != nil {
			return "", err
		}
		out = append(out, set[i])
	}
	for i := len(out) - 1; i > 0; i-- {
		j, err := n(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

// SetTemporaryPassword replaces a user's password with a random one and
// returns it, to be shown once. Every session of the account ends and a
// sign-in lock is lifted, so the new password works straight away.
func (s *UserService) SetTemporaryPassword(ctx context.Context, userID uuid.UUID) (string, error) {
	password, err := GenerateTemporaryPassword(LoadPasswordPolicy(s.db).MinLength)
	if err != nil {
		return "", err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}
	res := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", userID).
		Updates(map[string]interface{}{"password_hash": string(hashed), "token_revoked_at": time.Now()})
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", ErrUserNotFound
	}
	if err := ClearSignInLock(ctx, s.db, userID); err != nil {
		slog.Default().Warn("temporary password set, but the sign-in lock could not be lifted", "user_id", userID, "error", err)
	}
	return password, nil
}

// SendPasswordResetLink emails a user a link to choose a new password, on an
// administrator's request. Unlike the public "forgot password" form it says
// plainly when the mail could not be sent: the administrator is waiting on it.
func (s *UserService) SendPasswordResetLink(ctx context.Context, user *models.User) error {
	base := PublicURL()
	if base == "" {
		return ErrPublicURLNotSet
	}
	cfg, ok, err := LoadSMTPSettings(s.db)
	if err != nil {
		return err
	}
	if !ok {
		return ErrEmailNotConfigured
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	raw := hex.EncodeToString(buf)
	tok := &models.PasswordResetToken{ID: uuid.New(), UserID: user.ID, TokenHash: hashResetToken(raw), ExpiresAt: time.Now().Add(resetTokenTTL)}
	if err := repository.NewEnrollmentRepository(s.db).CreatePasswordResetToken(ctx, tok); err != nil {
		return err
	}
	if err := sendResetLinkNow(cfg, user.Email, user.Name, base+"/reset-password?token="+raw); err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	return nil
}

// sendResetLinkNow is a var so tests can capture the link.
var sendResetLinkNow = SendPasswordResetEmail

// NotifyEmailChanged tells the old address that an administrator moved the
// account to a new one, so a takeover by that route does not pass unseen.
// Best-effort, and off the request: a slow mail server must not hold it up.
var NotifyEmailChanged = func(db *gorm.DB, oldEmail, newEmail, name string) {
	cfg, ok, err := LoadSMTPSettings(db)
	if err != nil || !ok {
		slog.Default().Warn("account email changed, but the old address could not be told: email is not set up", "old_email", oldEmail)
		return
	}
	msg := RenderEmail(EmailTemplate{
		Subject: "The email address of your {{brand}} account was changed",
		Body: "{{greeting}},\n\nAn administrator changed the email address of your {{brand}} account from {{email}} to {{new_email}}.\n\n" +
			"You now sign in with the new address, and password-reset messages go there. If you did not ask for this, contact support.",
	}, cfg.Brand, EmailData{Name: name, Email: oldEmail, LogoURL: EmailLogoURL(cfg.Brand, PublicURL()),
		Extra: map[string]string{"new_email": newEmail}})
	if err := queueRendered(context.Background(), db, "email_changed", oldEmail, msg, emailRefs{}); err != nil {
		slog.Default().Warn("account email changed, but the old address could not be told", "old_email", oldEmail, "error", err)
	}
}
