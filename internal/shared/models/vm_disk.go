package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VMDiskLifecycle enumerates the lifecycle states of an extra data disk as it is
// detached/destroyed. It is distinct from the soft-delete marker (DeletedAt):
// lifecycle tracks the storage reclaim intent ('attached' until a detach/destroy
// is verified by the agent), while DeletedAt is only set by the panel worker
// AFTER the agent has certified the backing volume is physically gone.
const (
	// VMDiskLifecycleAttached is the normal state: the disk is live on the node.
	VMDiskLifecycleAttached = "attached"
	// VMDiskLifecycleDeleting marks a disk whose detach/destroy has been requested
	// and is pending agent confirmation; accounting still counts it until verified.
	VMDiskLifecycleDeleting = "deleting"
)

// VMDisk is an extra data disk attached to a VM (the primary boot disk is not
// tracked here — only additional disks added/removed via the disks API).
type VMDisk struct {
	ID        string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string    `json:"vm_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Device    string    `json:"device" gorm:"type:varchar(16);not null"`   // virtio target, e.g. vdb
	SizeGB    int       `json:"size_gb" gorm:"not null;check:size_gb > 0"` // strictly positive; positive DB protection
	Path      string    `json:"path" gorm:"type:text;not null"`            // backing volume path on the node
	Lifecycle string    `json:"lifecycle" gorm:"type:varchar(16);not null;default:'attached';check:lifecycle in ('attached','deleting')"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
	// DeletedAt is preserved: the panel worker sets it ONLY after the agent has
	// certified the volume is physically destroyed. This schema lane does NOT
	// globally change GORM delete behavior; later worker lifecycle explicitly
	// hard-deletes the row post-certification.
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for VMDisk.
func (VMDisk) TableName() string { return "vm_disks" }

// BeforeCreate hook for VMDisk.
func (d *VMDisk) BeforeCreate(tx *gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	return nil
}
