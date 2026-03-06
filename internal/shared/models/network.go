package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Network represents a network configuration for a VM
type Network struct {
	ID             string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID           string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	IPAddress      string         `json:"ip_address" gorm:"type:inet;not null" validate:"required,ip"`
	BandwidthLimit int64          `json:"bandwidth_limit" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	VLANID         *int           `json:"vlan_id" gorm:"type:integer" validate:"omitempty,min=1,max=4094"`
	CreatedAt      time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt      time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Network
func (Network) TableName() string {
	return "networks"
}

// BeforeCreate hook for Network
func (n *Network) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (n *Network) GetID() uuid.UUID {
	id, _ := uuid.Parse(n.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (n *Network) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(n.VMID)
	return id
}

// Validate validates the Network struct
func (n *Network) Validate() ValidationErrors {
	return ValidateStruct(n)
}
