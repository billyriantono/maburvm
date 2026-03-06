package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SnapshotStatus represents the status of a snapshot
type SnapshotStatus string

const (
	SnapshotStatusPending  SnapshotStatus = "pending"
	SnapshotStatusComplete SnapshotStatus = "completed"
	SnapshotStatusFailed   SnapshotStatus = "failed"
)

// Snapshot represents a VM snapshot
type Snapshot struct {
	ID        uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      uuid.UUID      `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Name      string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	DiskPath  string         `json:"disk_path" gorm:"type:varchar(500);not null" validate:"required,max=500"`
	Status    SnapshotStatus `json:"status" gorm:"type:snapshot_status;default:pending" validate:"required,oneof=pending completed failed"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Snapshot
func (Snapshot) TableName() string {
	return "snapshots"
}

// BeforeCreate hook for Snapshot
func (s *Snapshot) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.Status == "" {
		s.Status = SnapshotStatusPending
	}
	return nil
}

// Validate validates the Snapshot struct
func (s *Snapshot) Validate() ValidationErrors {
	return ValidateStruct(s)
}
