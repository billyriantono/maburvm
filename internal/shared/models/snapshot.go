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
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
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
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	if s.Status == "" {
		s.Status = SnapshotStatusPending
	}
	return nil
}

// GetID returns the UUID representation of ID
func (s *Snapshot) GetID() uuid.UUID {
	id, _ := uuid.Parse(s.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (s *Snapshot) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(s.VMID)
	return id
}

// Validate validates the Snapshot struct
func (s *Snapshot) Validate() ValidationErrors {
	return ValidateStruct(s)
}
