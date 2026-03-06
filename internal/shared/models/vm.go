package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VMStatus represents the status of a VM
type VMStatus string

const (
	VMStatusRunning   VMStatus = "running"
	VMStatusStopped   VMStatus = "stopped"
	VMStatusSuspended VMStatus = "suspended"
	VMStatusCreating  VMStatus = "creating"
	VMStatusError     VMStatus = "error"
)

// Resources represents the CPU, RAM, and Disk resources for a VM
type Resources struct {
	CPU  int  `json:"cpu" validate:"required,min=1,max=128"`
	RAM  int  `json:"ram" validate:"required,min=128,max=131072"` // MB
	Disk int  `json:"disk" validate:"required,min=1,max=1048576"` // GB
	IOPS *int `json:"iops,omitempty" validate:"omitempty,min=100,max=100000"`
	Swap *int `json:"swap,omitempty" validate:"omitempty,min=0,max=65536"`
}

// VM represents a virtual machine in the system
type VM struct {
	ID              uuid.UUID      `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID          uuid.UUID      `json:"user_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	NodeID          uuid.UUID      `json:"node_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Hostname        string         `json:"hostname" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	OSTemplateID    uuid.UUID      `json:"os_template_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Resources       Resources      `json:"resources" gorm:"type:jsonb;serializer:json" validate:"required"`
	Status          VMStatus       `json:"status" gorm:"type:vm_status;default:stopped" validate:"required,oneof=running stopped suspended creating error"`
	SourceMigration bool           `json:"source_migration" gorm:"default:FALSE"`
	VNCPort         *int           `json:"vnc_port" gorm:"type:integer" validate:"omitempty,min=5900,max=5999"`
	VNCPassword     string         `json:"-" gorm:"type:varchar(255)"` // Never exposed in JSON
	CreatedAt       time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt       time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for VM
func (VM) TableName() string {
	return "vms"
}

// BeforeCreate hook for VM
func (v *VM) BeforeCreate(tx *gorm.DB) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	if v.Status == "" {
		v.Status = VMStatusStopped
	}
	return nil
}

// Validate validates the VM struct
func (v *VM) Validate() ValidationErrors {
	return ValidateStruct(v)
}
