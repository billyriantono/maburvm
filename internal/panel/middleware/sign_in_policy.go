package middleware

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SignInPolicy is the sign-in half of Settings, System, Security. It shares
// the 'security' settings row with the password policy.
type SignInPolicy struct {
	// LockoutAttempts is how many wrong passwords or codes in a row lock an
	// account. 0 turns lockout off.
	LockoutAttempts int `json:"lockoutAttempts"`
	// LockoutMinutes is how long a locked account stays locked.
	LockoutMinutes int `json:"lockoutMinutes"`
	// RequireStaff2FA holds an administrator without two-factor to the pages
	// that set it up.
	RequireStaff2FA bool `json:"requireStaff2fa"`
	// SessionHours is how long a sign-in lasts.
	SessionHours int `json:"sessionHours"`
	// CaptchaProvider ("turnstile" or "hcaptcha") with its keys puts a check
	// on sign-in and password reset after CaptchaAfterFailures failed attempts
	// from one address or for one email in CaptchaWindow (0 = always). The
	// secret is sealed at rest. Off unless all three are set.
	CaptchaProvider      string `json:"captchaProvider"`
	CaptchaSiteKey       string `json:"captchaSiteKey"`
	CaptchaSecret        string `json:"captchaSecret"`
	CaptchaAfterFailures int    `json:"captchaAfterFailures"`
}

// CaptchaWindow is how far back failed attempts count towards the captcha.
const CaptchaWindow = 15 * time.Minute

// CaptchaOn reports whether the sign-in captcha is set up.
func (p SignInPolicy) CaptchaOn() bool {
	return (p.CaptchaProvider == "turnstile" || p.CaptchaProvider == "hcaptcha") &&
		p.CaptchaSiteKey != "" && p.CaptchaSecret != ""
}

// DefaultSignInPolicy is what applies until an operator saves something else.
// The web form's defaults must mirror it.
func DefaultSignInPolicy() SignInPolicy {
	return SignInPolicy{LockoutAttempts: 10, LockoutMinutes: 15, SessionHours: int(AccessTokenExpiry / time.Hour), CaptchaAfterFailures: 3}
}

// LoadSignInPolicy reads the operator's sign-in policy. A field never saved
// keeps its built-in value, and a value out of range is pulled back in.
func LoadSignInPolicy(db *gorm.DB) SignInPolicy {
	p := DefaultSignInPolicy()
	if db == nil {
		return p
	}
	var raw string
	if err := db.Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = 'security'").Scan(&raw).Error; err != nil || raw == "" {
		return p
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return DefaultSignInPolicy()
	}
	if p.LockoutAttempts != 0 {
		p.LockoutAttempts = min(max(p.LockoutAttempts, 3), 100)
	}
	p.LockoutMinutes = min(max(p.LockoutMinutes, 1), 24*60)
	p.SessionHours = min(max(p.SessionHours, 1), 30*24)
	p.CaptchaAfterFailures = min(max(p.CaptchaAfterFailures, 0), 100)
	return p
}

// SessionLifetime is how long a new access token is valid.
func (p SignInPolicy) SessionLifetime() time.Duration {
	return time.Duration(p.SessionHours) * time.Hour
}

// twoFactorSetupPaths are what an administrator who still has to set up
// two-factor may reach: who they are, signing out, and their own security.
var twoFactorSetupPaths = []string{
	"/api/v1/auth/me",
	"/api/v1/auth/logout",
	"/api/v1/settings/profile",
	"/api/v1/settings/change-password",
	"/api/v1/settings/2fa/",
}

func isTwoFactorSetupPath(path string) bool {
	for _, p := range twoFactorSetupPaths {
		if path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(path, p)) {
			return true
		}
	}
	return false
}
