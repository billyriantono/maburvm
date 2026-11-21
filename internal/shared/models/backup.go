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

	// Disks are every disk the backup holds, primary first. Empty for a backup
	// of the primary disk only (StoragePath), as all older ones are.
	Disks []BackupDisk `json:"disks" gorm:"type:jsonb;serializer:json"`
	// Consistency is how the copy was taken (BackupConsistency*); empty for a
	// backup made before it was recorded, which was read while the VM ran.
	Consistency string `json:"consistency" gorm:"type:varchar(20)"`

	// DestinationID is the destination holding the files; nil for a backup in
	// a node's own storage.
	DestinationID *string `json:"destination_id,omitempty" gorm:"type:uuid"`
	// NodeID is the node that wrote it: the one that can reach a directory
	// mounted on it alone.
	NodeID *string `json:"node_id,omitempty" gorm:"type:uuid"`
	// Mode is BackupModeFull or BackupModeIncremental. An incremental one
	// holds only what changed since ParentID, and needs it to be restored.
	Mode     string  `json:"mode" gorm:"type:varchar(12);not null;default:full"`
	ParentID *string `json:"parent_id,omitempty" gorm:"type:uuid"`
	// ChainID is the full backup a chain starts from (its own ID for a full one).
	ChainID *string `json:"chain_id,omitempty" gorm:"type:uuid"`
	// Checkpoint is the libvirt checkpoint the backup left on its VM, which the
	// next incremental one copies the changes since. Empty: none.
	Checkpoint string `json:"-" gorm:"type:varchar(100);not null;default:''"`
	// ModeNote says why a backup is full when an incremental one was asked.
	ModeNote string `json:"mode_note,omitempty" gorm:"type:text;not null;default:''"`
}

// Backup modes.
const (
	BackupModeFull        = "full"
	BackupModeIncremental = "incremental"
)

// BackupCheckpointPrefix starts the name of every checkpoint a backup leaves
// on its VM; the agent removes only those.
const BackupCheckpointPrefix = "maburvm-"

// TableName specifies the table name for Backup
func (Backup) TableName() string {
	return "backups"
}

// BackupDisk is one disk of a backup.
type BackupDisk struct {
	Target      string `json:"target"` // vda, vdb...
	Key         string `json:"key"`    // where its image is in storage
	Size        int64  `json:"size"`
	Checksum    string `json:"checksum"`
	VirtualSize int64  `json:"virtual_size,omitempty"` // as the guest sees it
}

// How a backup's copy was taken.
const (
	BackupConsistencyStopped = "stopped" // the VM was off
	BackupConsistencyFrozen  = "frozen"  // snapshot with the guest's file systems frozen
	BackupConsistencyCrash   = "crash"   // snapshot without a freeze, like a power cut
	BackupConsistencyLive    = "live"    // read while the VM wrote to it; may not be usable
)

// StorageKeys lists every object the backup is stored as.
func (b *Backup) StorageKeys() []string {
	keys := []string{b.StoragePath}
	for _, d := range b.Disks {
		if d.Key != "" && d.Key != b.StoragePath {
			keys = append(keys, d.Key)
		}
	}
	return keys
}

// DataDisks are the backup's disks after the primary one.
func (b *Backup) DataDisks() []BackupDisk {
	if len(b.Disks) < 2 {
		return nil
	}
	return b.Disks[1:]
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
	if b.Mode == "" {
		b.Mode = BackupModeFull
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
	// MaxAgeDays deletes backups older than this many days; 0 = no age limit.
	MaxAgeDays int `json:"max_age_days" validate:"omitempty,min=0,max=3650"`
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
	// FullEveryDays > 0 makes scheduled backups incremental where the node
	// can, with a full one at least that often. 0: every one is full.
	FullEveryDays int `json:"full_every_days" gorm:"not null;default:0"`
	// ChainResetAt is when the VM's disks were last replaced (restore,
	// reinstall): no backup from before may be built on.
	ChainResetAt *time.Time     `json:"-"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

// MaxFullEveryDays bounds a chain: every incremental backup in it is
// downloaded to restore the newest.
const MaxFullEveryDays = 31

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

// Where a finished backup's file is, recorded in Backup.StorageProvider.
const (
	// BackupStoreDestination: the destination set in the panel, which every
	// node and the panel itself can reach.
	BackupStoreDestination = "s3"
	// BackupStoreNode: storage only the VM's node knows, from its environment
	// file. Backups made before the panel had a destination, or by an agent
	// too old to be given one.
	BackupStoreNode = "node"
)
