package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/secret"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// The first administrator of a new install is created in the browser with a
// one-time token. The token file exists only while the panel has no
// administrator and lives on the panel's data volume, so only someone with
// access to the host can read it; scripts/install-panel.sh prints the link.
const firstRunTokenFile = "first-run-token"

// firstRunLock serialises first-administrator creation (pg_advisory_xact_lock).
const firstRunLock = 742001

var (
	// ErrFirstRunClosed: the panel already has an administrator.
	ErrFirstRunClosed = errors.New("this panel already has an administrator; sign in instead")
	// ErrFirstRunToken: the token is missing or wrong.
	ErrFirstRunToken = errors.New("this setup link is not valid. Use the link the installer printed, or read the token in first-run-token on the panel's data volume")
)

func firstRunTokenPath() string { return filepath.Join(secret.DataDir(), firstRunTokenFile) }

func hasAdmin(db *gorm.DB) (bool, error) {
	var n int64
	err := db.Raw("SELECT COUNT(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL").Scan(&n).Error
	return n > 0, err
}

// PrepareFirstRun keeps the token file in step with the users table: written
// when there is no administrator, removed once there is one. It reports
// whether first-run setup is open.
func PrepareFirstRun(ctx context.Context, db *gorm.DB) (bool, error) {
	has, err := hasAdmin(db.WithContext(ctx))
	if err != nil {
		return false, err
	}
	path := firstRunTokenPath()
	if has {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return true, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := fmt.Fprintln(f, hex.EncodeToString(b)); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

// FirstRunOpen reports whether the first administrator can still be created.
func FirstRunOpen(ctx context.Context, db *gorm.DB) bool {
	has, err := hasAdmin(db.WithContext(ctx))
	if err != nil || has {
		return false
	}
	_, err = os.Stat(firstRunTokenPath())
	return err == nil
}

// CreateFirstAdmin creates the founding administrator, once, for whoever
// holds the token, and then removes the token.
func CreateFirstAdmin(ctx context.Context, db *gorm.DB, token, name, email, password string) (*models.User, error) {
	want, err := os.ReadFile(firstRunTokenPath())
	if err != nil {
		if has, herr := hasAdmin(db.WithContext(ctx)); herr == nil && has {
			return nil, ErrFirstRunClosed
		}
		return nil, ErrFirstRunToken
	}
	got := strings.TrimSpace(token)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(strings.TrimSpace(string(want)))) != 1 {
		return nil, ErrFirstRunToken
	}
	name, email = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return nil, errors.New("enter a valid email address")
	}
	if name == "" || len(name) > 255 {
		return nil, errors.New("enter a name of up to 255 characters")
	}
	if err := ValidatePasswordStrength(db.WithContext(ctx), password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return nil, err
	}
	user := &models.User{Name: name, Email: email, PasswordHash: string(hash), Role: models.RoleAdmin,
		IPWhitelist: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", firstRunLock).Error; err != nil {
			return err
		}
		if has, err := hasAdmin(tx); err != nil {
			return err
		} else if has {
			return ErrFirstRunClosed
		}
		var taken int64
		if err := tx.Raw("SELECT COUNT(*) FROM users WHERE LOWER(email) = ? AND deleted_at IS NULL", email).Scan(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return errors.New("an account with that email already exists")
		}
		return tx.Create(user).Error
	})
	if err != nil {
		return nil, err
	}
	_ = os.Remove(firstRunTokenPath())
	return user, nil
}
