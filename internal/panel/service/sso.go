package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ErrNoSSOAccount means the identity provider vouched for an email that has
// no panel user. SSO never creates users; an admin adds them first.
var ErrNoSSOAccount = errors.New("no panel user for this email")

// SSOLogin signs in a user whose email an OIDC provider has verified. The
// same rules as a password login apply: the IP whitelist, and the panel's own
// two-factor step when the user has it enabled (needs2FA, no token yet).
func (s *UserService) SSOLogin(email, clientIP, userAgent string) (user *models.User, token string, needs2FA bool, err error) {
	var u models.User
	if err := s.db.Where("LOWER(email) = ?", strings.ToLower(email)).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", false, ErrNoSSOAccount
		}
		return nil, "", false, fmt.Errorf("database error: %w", err)
	}
	// A temporary account that has run out is not an account any more.
	if u.Expired() {
		return nil, "", false, ErrNoSSOAccount
	}
	if u.Suspended() {
		return nil, "", false, ErrAccountSuspended
	}
	if len(u.IPWhitelist) > 0 && !s.isIPWhitelisted(clientIP, u.IPWhitelist) {
		return nil, "", false, ErrIPNotWhitelisted
	}
	if u.TwoFactorEnabled {
		return &u, "", true, nil
	}
	token, err = s.mintSession(&u, middleware.SessionMeta{Method: middleware.SignInSSO, IP: clientIP, UserAgent: userAgent})
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to generate token: %w", err)
	}
	auditSupportSignIn(s.db, &u, clientIP)
	return &u, token, false, nil
}

// BillingSSOAccountError says why the billing system may not sign an account
// in, nil when it may: a customer account that has not run out, is not
// suspended and is not locked after failed sign-ins. A billing system never
// signs anyone in as staff.
func BillingSSOAccountError(u *models.User) error {
	switch {
	case u.Expired(), u.Role != models.RoleClient:
		return ErrNoSSOAccount
	case u.Suspended():
		return ErrAccountSuspended
	case u.SignInLocked():
		return &AccountLockedError{Until: *u.LockedUntil}
	}
	return nil
}

// BillingSSOLogin signs in the customer a billing system vouched for with a
// one-time link. The account rules of BillingSSOAccountError, the IP
// whitelist and the panel's own two-factor step apply. The user is returned
// with a refusal too, for the sign-in history.
func (s *UserService) BillingSSOLogin(userID uuid.UUID, clientIP, userAgent string) (user *models.User, token string, needs2FA bool, err error) {
	var u models.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", false, ErrNoSSOAccount
		}
		return nil, "", false, fmt.Errorf("database error: %w", err)
	}
	if err := BillingSSOAccountError(&u); err != nil {
		return &u, "", false, err
	}
	if len(u.IPWhitelist) > 0 && !s.isIPWhitelisted(clientIP, u.IPWhitelist) {
		return &u, "", false, ErrIPNotWhitelisted
	}
	if u.TwoFactorEnabled {
		return &u, "", true, nil
	}
	token, err = s.mintSession(&u, middleware.SessionMeta{Method: middleware.SignInBillingSSO, IP: clientIP, UserAgent: userAgent})
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to generate token: %w", err)
	}
	auditSupportSignIn(s.db, &u, clientIP)
	return &u, token, false, nil
}

// SSOVerify2FA finishes an SSO sign-in (method SignInSSO or SignInBillingSSO)
// that stopped at the two-factor step.
func (s *UserService) SSOVerify2FA(userID uuid.UUID, code, clientIP, userAgent, method string) (*models.User, string, error) {
	var u models.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil || u.Locked() {
		return nil, "", ErrInvalidCredentials
	}
	if method == middleware.SignInBillingSSO && u.Role != models.RoleClient {
		return nil, "", ErrInvalidCredentials
	}
	if len(u.IPWhitelist) > 0 && !s.isIPWhitelisted(clientIP, u.IPWhitelist) {
		return nil, "", ErrIPNotWhitelisted
	}
	if !u.TwoFactorEnabled {
		return nil, "", ErrInvalidTOTPCode
	}
	if err := s.checkTwoFactor(&u, code, clientIP); err != nil {
		return nil, "", err
	}
	token, err := s.mintSession(&u, middleware.SessionMeta{Method: method, IP: clientIP, UserAgent: userAgent})
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate token: %w", err)
	}
	auditSupportSignIn(s.db, &u, clientIP)
	return &u, token, nil
}
