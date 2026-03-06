package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FirewallRule represents a firewall rule for a VM
type FirewallRule struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Protocol  string         `json:"protocol" gorm:"type:varchar(10);not null" validate:"required,oneof=tcp udp icmp all"`
	PortRange string         `json:"port_range" gorm:"type:varchar(50)" validate:"omitempty,port_range"`
	Action    string         `json:"action" gorm:"type:varchar(10);not null" validate:"required,oneof=allow deny"`
	Direction string         `json:"direction" gorm:"type:varchar(10);not null" validate:"required,oneof=inbound outbound"`
	SourceIP  string         `json:"source_ip" gorm:"type:cidr;default:0.0.0.0/0" validate:"omitempty,ip_or_cidr"`
	Priority  int            `json:"priority" gorm:"type:integer;default:100" validate:"required,min=1,max=1000"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for FirewallRule
func (FirewallRule) TableName() string {
	return "firewall_rules"
}

// BeforeCreate hook for FirewallRule
func (f *FirewallRule) BeforeCreate(tx *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (f *FirewallRule) GetID() uuid.UUID {
	id, _ := uuid.Parse(f.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (f *FirewallRule) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(f.VMID)
	return id
}

// Validate validates the FirewallRule struct
func (f *FirewallRule) Validate() ValidationErrors {
	return ValidateStruct(f)
}
