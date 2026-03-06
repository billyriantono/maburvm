package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Session represents a user session
type Session struct {
	ID        string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID    string    `json:"user_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Token     string    `json:"-" gorm:"type:varchar(500);uniqueIndex;not null"` // Never exposed in JSON
	ExpiresAt time.Time `json:"expires_at" gorm:"not null" validate:"required"`
	IPAddress string    `json:"ip_address" gorm:"type:inet" validate:"omitempty,ip"`
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
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (s *Session) GetID() uuid.UUID {
	id, _ := uuid.Parse(s.ID)
	return id
}

// GetUserID returns the UUID representation of UserID
func (s *Session) GetUserID() uuid.UUID {
	id, _ := uuid.Parse(s.UserID)
	return id
}

// Validate validates the Session struct
func (s *Session) Validate() ValidationErrors {
	return ValidateStruct(s)
}

// IsExpired checks if the session has expired
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}
