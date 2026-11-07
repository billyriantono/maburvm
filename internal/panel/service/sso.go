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
func (s *UserService) SSOLogin(email, clientIP string) (user *models.User, token string, needs2FA bool, err error) {
	var u models.User
	if err := s.db.Where("LOWER(email) = ?", strings.ToLower(email)).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", false, ErrNoSSOAccount
		}
		return nil, "", false, fmt.Errorf("database error: %w", err)
	}
	if len(u.IPWhitelist) > 0 && !s.isIPWhitelisted(clientIP, u.IPWhitelist) {
		return nil, "", false, ErrIPNotWhitelisted
	}
	if u.TwoFactorEnabled {
		return &u, "", true, nil
	}
	token, err = middleware.GenerateAccessToken(&u)
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to generate token: %w", err)
	}
	return &u, token, false, nil
}

// SSOVerify2FA finishes an SSO sign-in that stopped at the two-factor step.
func (s *UserService) SSOVerify2FA(userID uuid.UUID, code, clientIP string) (*models.User, string, error) {
	var u models.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return nil, "", ErrInvalidCredentials
	}
	if len(u.IPWhitelist) > 0 && !s.isIPWhitelisted(clientIP, u.IPWhitelist) {
		return nil, "", ErrIPNotWhitelisted
	}
	if !u.TwoFactorEnabled || !s.verifyTwoFactorCode(&u, code) {
		return nil, "", ErrInvalidTOTPCode
	}
	token, err := middleware.GenerateAccessToken(&u)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate token: %w", err)
	}
	return &u, token, nil
}
