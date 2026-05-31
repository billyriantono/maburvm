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
	ID              string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID          string         `json:"user_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	NodeID          string         `json:"node_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Hostname        string         `json:"hostname" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	OSTemplateID    string         `json:"os_template_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Resources       Resources      `json:"resources" gorm:"type:jsonb;serializer:json" validate:"required"`
	Status          VMStatus       `json:"status" gorm:"type:vm_status;default:stopped" validate:"required,oneof=running stopped suspended creating error"`
	SourceMigration string         `json:"source_migration,omitempty" gorm:"type:varchar(50);default:null"`
	VNCPort         *int           `json:"vnc_port" gorm:"type:integer" validate:"omitempty,min=5900,max=5999"`
	VNCPassword     string         `json:"-" gorm:"type:varchar(255)"` // Never exposed in JSON
	ConsoleEnabled  bool           `json:"console_enabled" gorm:"column:console_enabled;not null;default:true"`
	RescueMode      bool           `json:"rescue_mode" gorm:"column:rescue_mode;not null;default:false"`
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
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	if v.Status == "" {
		v.Status = VMStatusStopped
	}
	return nil
}

// GetID returns the UUID representation of ID
func (v *VM) GetID() uuid.UUID {
	id, _ := uuid.Parse(v.ID)
	return id
}

// GetUserID returns the UUID representation of UserID
func (v *VM) GetUserID() uuid.UUID {
	id, _ := uuid.Parse(v.UserID)
	return id
}

// GetNodeID returns the UUID representation of NodeID
func (v *VM) GetNodeID() uuid.UUID {
	id, _ := uuid.Parse(v.NodeID)
	return id
}

// GetOSTemplateID returns the UUID representation of OSTemplateID
func (v *VM) GetOSTemplateID() uuid.UUID {
	id, _ := uuid.Parse(v.OSTemplateID)
	return id
}

// Validate validates the VM struct
func (v *VM) Validate() ValidationErrors {
	return ValidateStruct(v)
}
