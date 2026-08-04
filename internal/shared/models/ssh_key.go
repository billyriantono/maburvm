package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SSHKey is a user's saved SSH public key, selectable when creating or
// rebuilding a VM (parity). Only public keys are ever stored.
type SSHKey struct {
	ID          string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID      string         `json:"user_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Name        string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	PublicKey   string         `json:"public_key" gorm:"type:text;not null" validate:"required"`
	Fingerprint string         `json:"fingerprint" gorm:"type:varchar(128);not null;index"` // SHA256:...
	CreatedAt   time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for SSHKey.
func (SSHKey) TableName() string { return "ssh_keys" }

// BeforeCreate hook for SSHKey.
func (k *SSHKey) BeforeCreate(tx *gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.New().String()
	}
	return nil
}
