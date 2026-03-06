package models

import (
	"net"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Session represents a user session
type Session struct {
	ID        uuid.UUID `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Token     string    `json:"-" gorm:"type:varchar(500);uniqueIndex;not null"` // Never exposed in JSON
	ExpiresAt time.Time `json:"expires_at" gorm:"not null" validate:"required"`
	IPAddress net.IP    `json:"ip_address" gorm:"type:inet"`
	UserAgent string    `json:"user_agent" gorm:"type:varchar(500)" validate:"omitempty,max=500"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for Session
func (Session) TableName() string {
	return "sessions"
}

// BeforeCreate hook for Session
func (s *Session) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// Validate validates the Session struct
func (s *Session) Validate() ValidationErrors {
	return ValidateStruct(s)
}

// IsExpired checks if the session has expired
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}
