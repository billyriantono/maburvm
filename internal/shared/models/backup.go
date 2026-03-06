package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BackupStatus represents the status of a backup
type BackupStatus string

const (
	BackupStatusPending    BackupStatus = "pending"
	BackupStatusInProgress BackupStatus = "in_progress"
	BackupStatusComplete   BackupStatus = "completed"
	BackupStatusFailed     BackupStatus = "failed"
)

// Backup represents a VM backup
type Backup struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID            uuid.UUID      `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	StorageProvider string         `json:"storage_provider" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Status          BackupStatus   `json:"status" gorm:"type:backup_status;default:pending" validate:"required,oneof=pending in_progress completed failed"`
	Size            int64          `json:"size" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	CreatedAt       time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt       time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Backup
func (Backup) TableName() string {
	return "backups"
}

// BeforeCreate hook for Backup
func (b *Backup) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	if b.Status == "" {
		b.Status = BackupStatusPending
	}
	return nil
}

// Validate validates the Backup struct
func (b *Backup) Validate() ValidationErrors {
	return ValidateStruct(b)
}
