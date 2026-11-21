package models

import (
	"net"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserRole represents the role of a user in the system
type UserRole string

const (
	RoleAdmin  UserRole = "admin"
	RoleClient UserRole = "client"
)

// User represents a user in the system
type User struct {
	ID           uuid.UUID `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name         string    `json:"name" gorm:"column:name;type:varchar(255);not null;default:''" validate:"omitempty,max=255"`
	Email        string    `json:"email" gorm:"type:varchar(255);uniqueIndex;not null" validate:"required,email"`
	PasswordHash string    `json:"-" gorm:"type:varchar(255);not null"` // Never exposed in JSON
	Role         UserRole  `json:"role" gorm:"type:user_role;default:client" validate:"required,oneof=admin client"`
	// QuotaMode is the user-level legacy/managed marker. It defaults to legacy
	// for every existing user (migration 037) so a managed user without a
	// user_quotas row cannot be mistaken for a legacy user who simply has no
	// quota row. Managed quota assignment/enforcement reads this marker.
	QuotaMode       QuotaMode `json:"quota_mode,omitempty" gorm:"column:quota_mode;type:quota_mode;not null;default:'legacy'"`
	TwoFactorSecret string    `json:"-" gorm:"type:varchar(255)" validate:"-"`
	// TwoFactorEnabled becomes true only after Verify2FASetup confirms a TOTP
	// code. Login gates on THIS flag, not on TwoFactorSecret presence, so an
	// abandoned setup (secret written, never verified) can't lock the user out.
	TwoFactorEnabled bool `json:"two_factor_enabled" gorm:"column:two_factor_enabled;not null;default:false"`
	// ExpiresAt is set only on temporary accounts (support access): past it,
	// the account cannot sign in and its sessions and API keys stop working.
	// Read-only here; written by table name when the access is granted.
	ExpiresAt *time.Time `json:"expires_at,omitempty" gorm:"column:expires_at;->"`
	// SuspendedAt is set while an administrator or the billing system has
	// suspended the account: no sign-in, and sessions and API keys stop working.
	// Read-only here; service.SetAccountSuspended is the writer.
	SuspendedAt *time.Time `json:"suspended_at,omitempty" gorm:"column:suspended_at;->"`
	// Sign-in lockout and authenticator-code replay state. Read-only here so a
	// full-row save cannot write back a stale value; written by table name.
	FailedLoginCount     int        `json:"-" gorm:"column:failed_login_count;->"`
	LockedUntil          *time.Time `json:"locked_until,omitempty" gorm:"column:locked_until;->"`
	TOTPLastStep         int64      `json:"-" gorm:"column:totp_last_step;->"`
	TwoFactorBackupCodes string     `json:"-" gorm:"type:varchar(1000)"` // Encrypted backup codes
	IPWhitelist          []string   `json:"ip_whitelist" gorm:"type:jsonb;serializer:json" validate:"omitempty,dive,ip"`
	CreatedAt            time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt            time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
	// StaffRole is a staff account's role key (nil = administrator). Read-only
	// here; written by table name so a stale full-row save cannot revert it.
	StaffRole *string `json:"staff_role,omitempty" gorm:"column:staff_role;->"`
	// StaffRoleName is filled for display, not stored.
	StaffRoleName string `json:"staff_role_name,omitempty" gorm:"-"`
	// TokenRevokedAt is the logout revocation cutoff. RequireAuth rejects any
	// access token whose iat predates this instant, so logout invalidates all of a
	// user's outstanding stateless JWTs server-side. NULL = nothing revoked.
	TokenRevokedAt *time.Time     `json:"-" gorm:"column:token_revoked_at"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for User
func (User) TableName() string {
	return "users"
}

// BeforeCreate hook for User
func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.Role == "" {
		u.Role = RoleClient
	}
	return nil
}

// Validate validates the User struct
func (u *User) Validate() ValidationErrors {
	return ValidateStruct(u)
}

// ValidateIPWhitelist validates IP whitelist entries
func ValidateIPWhitelist(ips []string) error {
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		// Basic IP validation - allow IPv4 and IPv6
		if net.ParseIP(ip) == nil {
			return &ValidationError{Field: "ip_whitelist", Message: "invalid IP address: " + ip}
		}
	}
	return nil
}

// Expired reports whether a temporary account has run out.
func (u *User) Expired() bool {
	return u.ExpiresAt != nil && time.Now().After(*u.ExpiresAt)
}

// Suspended reports an account that has been suspended.
func (u *User) Suspended() bool {
	return u.SuspendedAt != nil
}

// SignInLocked reports an account locked after too many failed sign-ins. It
// stops new sign-ins only; open sessions and API keys keep working.
func (u *User) SignInLocked() bool {
	return u.LockedUntil != nil && time.Now().Before(*u.LockedUntil)
}

// Locked reports an account that may not be used right now: a temporary one
// that ran out, or a suspended one.
func (u *User) Locked() bool {
	return u.Expired() || u.Suspended()
}
