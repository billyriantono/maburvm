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
	BackupStatusCompleted  BackupStatus = "completed"
	BackupStatusFailed     BackupStatus = "failed"
)

// BackupType represents the type of backup
type BackupType string

const (
	BackupTypeManual   BackupType = "manual"
	BackupTypeSchedule BackupType = "scheduled"
)

// Backup represents a VM backup
type Backup struct {
	ID              string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID            string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	VM              VM             `json:"vm,omitempty" gorm:"foreignKey:VMID"`
	StorageProvider string         `json:"storage_provider" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	StoragePath     string         `json:"storage_path" gorm:"type:varchar(500);not null" validate:"required,max=500"`
	BackupType      BackupType     `json:"backup_type" gorm:"type:backup_type;default:manual" validate:"required,oneof=manual scheduled"`
	Status          BackupStatus   `json:"status" gorm:"type:backup_status;default:pending" validate:"required,oneof=pending in_progress completed failed"`
	Size            int64          `json:"size" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	Compression     string         `json:"compression" gorm:"type:varchar(20);default:gzip" validate:"omitempty,oneof=gzip zstd none"`
	Checksum        string         `json:"checksum" gorm:"type:varchar(64)" validate:"omitempty,max=64"`
	ErrorMessage    string         `json:"error_message,omitempty" gorm:"type:text"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
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
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	if b.Status == "" {
		b.Status = BackupStatusPending
	}
	if b.BackupType == "" {
		b.BackupType = BackupTypeManual
	}
	if b.Compression == "" {
		b.Compression = "gzip"
	}
	return nil
}

// GetID returns the UUID representation of ID
func (b *Backup) GetID() uuid.UUID {
	id, _ := uuid.Parse(b.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (b *Backup) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(b.VMID)
	return id
}

// Validate validates the Backup struct
func (b *Backup) Validate() ValidationErrors {
	return ValidateStruct(b)
}

// BackupScheduleStatus represents the status of a backup schedule
type BackupScheduleStatus string

const (
	BackupScheduleStatusActive   BackupScheduleStatus = "active"
	BackupScheduleStatusPaused   BackupScheduleStatus = "paused"
	BackupScheduleStatusDisabled BackupScheduleStatus = "disabled"
)

// BackupRetentionPolicy represents the retention policy for backups
type BackupRetentionPolicy struct {
	KeepLast    int `json:"keep_last" validate:"omitempty,min=0,max=100"`   // Keep last N backups
	KeepDaily   int `json:"keep_daily" validate:"omitempty,min=0,max=90"`   // Keep daily backups for N days
	KeepWeekly  int `json:"keep_weekly" validate:"omitempty,min=0,max=52"`  // Keep weekly backups for N weeks
	KeepMonthly int `json:"keep_monthly" validate:"omitempty,min=0,max=36"` // Keep monthly backups for N months
}

// BackupSchedule represents a scheduled backup configuration for a VM
type BackupSchedule struct {
	ID              string                `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID            string                `json:"vm_id" gorm:"type:uuid;not null;uniqueIndex" validate:"required,uuid"`
	VM              VM                    `json:"vm,omitempty" gorm:"foreignKey:VMID"`
	Schedule        string                `json:"schedule" gorm:"type:varchar(100);not null" validate:"required,max=100"` // Cron expression
	Status          BackupScheduleStatus  `json:"status" gorm:"type:backup_schedule_status;default:active" validate:"required,oneof=active paused disabled"`
	StorageProvider string                `json:"storage_provider" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Compression     string                `json:"compression" gorm:"type:varchar(20);default:gzip" validate:"omitempty,oneof=gzip zstd none"`
	RetentionPolicy BackupRetentionPolicy `json:"retention_policy" gorm:"type:jsonb;serializer:json"`
	NextRunAt       *time.Time            `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time            `json:"last_run_at,omitempty"`
	LastBackupID    *string               `json:"last_backup_id,omitempty" gorm:"type:uuid"`
	CreatedAt       time.Time             `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt       time.Time             `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt       gorm.DeletedAt        `json:"-" gorm:"index"`
}

// TableName specifies the table name for BackupSchedule
func (BackupSchedule) TableName() string {
	return "backup_schedules"
}

// BeforeCreate hook for BackupSchedule
func (bs *BackupSchedule) BeforeCreate(tx *gorm.DB) error {
	if bs.ID == "" {
		bs.ID = uuid.New().String()
	}
	if bs.Status == "" {
		bs.Status = BackupScheduleStatusActive
	}
	if bs.Compression == "" {
		bs.Compression = "gzip"
	}
	return nil
}

// GetID returns the UUID representation of ID
func (bs *BackupSchedule) GetID() uuid.UUID {
	id, _ := uuid.Parse(bs.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (bs *BackupSchedule) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(bs.VMID)
	return id
}

// Validate validates the BackupSchedule struct
func (bs *BackupSchedule) Validate() ValidationErrors {
	return ValidateStruct(bs)
}

// IsActive returns true if the schedule is active
func (bs *BackupSchedule) IsActive() bool {
	return bs.Status == BackupScheduleStatusActive
}
