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
	ID       string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID     string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Name     string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	DiskPath string         `json:"disk_path" gorm:"type:varchar(500);not null" validate:"required,max=500"`
	Status   SnapshotStatus `json:"status" gorm:"type:snapshot_status;default:pending" validate:"required,oneof=pending completed failed"`
	// DiskOnly: taken without the VM's memory. ScheduleID is the schedule
	// that took it, nil for one taken by hand. Read-only here, written by
	// table name.
	DiskOnly   bool           `json:"disk_only" gorm:"column:disk_only;->"`
	ScheduleID *string        `json:"schedule_id,omitempty" gorm:"column:schedule_id;->"`
	CreatedAt  time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt  time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt  gorm.DeletedAt `json:"-" gorm:"index"`
}

// SnapshotSchedule takes a snapshot of one VM every IntervalHours and keeps
// the newest Keep of those it took.
type SnapshotSchedule struct {
	ID            string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID          string     `json:"vm_id" gorm:"column:vm_id;type:uuid;not null"`
	IntervalHours int        `json:"interval_hours" gorm:"column:interval_hours;not null"`
	Keep          int        `json:"keep" gorm:"column:keep;not null"`
	DiskOnly      bool       `json:"disk_only" gorm:"column:disk_only;not null;default:true"`
	Quiesce       bool       `json:"quiesce" gorm:"column:quiesce;not null;default:false"`
	Enabled       bool       `json:"enabled" gorm:"column:enabled;not null;default:true"`
	NextRunAt     time.Time  `json:"next_run_at" gorm:"column:next_run_at;not null"`
	LastRunAt     *time.Time `json:"last_run_at" gorm:"column:last_run_at"`
	LastError     string     `json:"last_error" gorm:"column:last_error;not null;default:''"`
	CreatedAt     time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt     time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for SnapshotSchedule.
func (SnapshotSchedule) TableName() string { return "snapshot_schedules" }

// BeforeCreate hook for SnapshotSchedule.
func (s *SnapshotSchedule) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	return nil
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
