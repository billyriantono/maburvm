package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VMDisk is an extra data disk attached to a VM (the primary boot disk is not
// tracked here — only additional disks added/removed via the disks API).
type VMDisk struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string         `json:"vm_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Device    string         `json:"device" gorm:"type:varchar(16);not null"` // virtio target, e.g. vdb
	SizeGB    int            `json:"size_gb" gorm:"not null"`
	Path      string         `json:"path" gorm:"type:text;not null"` // backing volume path on the node
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
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
