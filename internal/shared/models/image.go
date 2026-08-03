package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ImageStatus is the lifecycle state of a stored image.
type ImageStatus string

const (
	ImageStatusPending   ImageStatus = "pending"   // export enqueued / in flight
	ImageStatusAvailable ImageStatus = "available" // uploaded to object storage, usable
	ImageStatusFailed    ImageStatus = "failed"
)

// Image is a user-owned, standalone disk image stored in object storage. It is
// captured from a VM's disk but survives that VM's deletion (SourceVMID is set
// NULL when the VM is removed), so it can seed a new VM later — the Vultr/DO
// "snapshot" model. See migration 047_images.
type Image struct {
	ID           uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID       uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;index"`
	Name         string         `json:"name" gorm:"type:varchar(255);not null;default:''"`
	SourceVMID   *uuid.UUID     `json:"source_vm_id,omitempty" gorm:"type:uuid"`
	OSTemplateID *uuid.UUID     `json:"os_template_id,omitempty" gorm:"type:uuid"`
	ObjectKey    string         `json:"-" gorm:"type:varchar(500);not null;default:''"`
	SizeBytes    int64          `json:"size_bytes" gorm:"not null;default:0"`
	Checksum     string         `json:"checksum" gorm:"type:varchar(64);not null;default:''"`
	Status       ImageStatus    `json:"status" gorm:"type:varchar(20);not null;default:'pending'"`
	ErrorMessage string         `json:"error_message,omitempty" gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Image.
func (Image) TableName() string { return "images" }

// BeforeCreate generates a UUID when one is not supplied.
func (i *Image) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}
