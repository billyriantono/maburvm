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
	QuotaMode            QuotaMode `json:"quota_mode,omitempty" gorm:"column:quota_mode;type:quota_mode;not null;default:'legacy'"`
	TwoFactorSecret      string    `json:"two_factor_secret,omitempty" gorm:"type:varchar(255)" validate:"-"`
	TwoFactorBackupCodes string    `json:"-" gorm:"type:varchar(1000)"` // Encrypted backup codes
	IPWhitelist          []string  `json:"ip_whitelist" gorm:"type:jsonb;serializer:json" validate:"omitempty,dive,ip"`
	CreatedAt            time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt            time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
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
