package models

import (
	"time"

	"github.com/google/uuid"
)

// UserSession is one sign-in. Its id rides in the access token, so revoking
// the row ends the session at its next request, cookie or not.
type UserSession struct {
	ID        uuid.UUID `json:"id" gorm:"type:uuid;primaryKey"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null"`
	Method    string    `json:"method" gorm:"type:varchar(20);not null"`
	IPAddress string    `json:"ip_address" gorm:"type:varchar(64);not null;default:''"`
	UserAgent string    `json:"user_agent" gorm:"type:varchar(500);not null;default:''"`
	// ImpersonatorID is the staff account viewing the panel as this user.
	ImpersonatorID *uuid.UUID `json:"impersonator_id,omitempty" gorm:"type:uuid"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

func (UserSession) TableName() string { return "user_sessions" }

// SignInEvent is one sign-in attempt or password reset request.
type SignInEvent struct {
	ID        uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey"`
	UserID    *uuid.UUID `json:"user_id,omitempty" gorm:"type:uuid"`
	Email     string     `json:"email"`
	Method    string     `json:"method"`
	Success   bool       `json:"success"`
	Note      string     `json:"note"`
	IPAddress string     `json:"ip_address"`
	UserAgent string     `json:"user_agent"`
	CreatedAt time.Time  `json:"created_at"`
}

func (SignInEvent) TableName() string { return "sign_in_events" }

// CustomStaffRole is a staff role an operator composed from permissions.
type CustomStaffRole struct {
	ID          string    `json:"id" gorm:"type:varchar(64);primaryKey"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions" gorm:"type:jsonb;serializer:json"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (CustomStaffRole) TableName() string { return "staff_roles" }
