package models

import (
	"net"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuditLog represents an audit log entry
type AuditLog struct {
	ID        uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID    *uuid.UUID     `json:"user_id" gorm:"type:uuid" validate:"omitempty,uuid"`
	Action    string         `json:"action" gorm:"type:varchar(255);not null" validate:"required,max=255"`
	IPAddress net.IP         `json:"ip_address" gorm:"type:inet"`
	Details   map[string]any `json:"details" gorm:"type:jsonb;serializer:json" validate:"-"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for AuditLog
func (AuditLog) TableName() string {
	return "audit_logs"
}

// BeforeCreate hook for AuditLog
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.Details == nil {
		a.Details = make(map[string]any)
	}
	return nil
}

// Validate validates the AuditLog struct
func (a *AuditLog) Validate() ValidationErrors {
	return ValidateStruct(a)
}
