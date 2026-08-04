package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ManagedNetwork is an administrator-defined virtual network (bridge/NAT/isolated)
// on a node — the "Network" concept (distinct from per-VM IP records
// and from IP pools).
type ManagedNetwork struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Type      string         `json:"type" gorm:"type:varchar(20);not null;default:'bridge'"` // bridge, nat, isolated
	Bridge    string         `json:"bridge" gorm:"type:varchar(50)"`
	Subnet    string         `json:"subnet" gorm:"type:varchar(64)"`
	Gateway   string         `json:"gateway" gorm:"type:varchar(64)"`
	DHCPStart string         `json:"dhcp_start" gorm:"column:dhcp_start;type:varchar(64)"`
	DHCPEnd   string         `json:"dhcp_end" gorm:"column:dhcp_end;type:varchar(64)"`
	VLANID    int            `json:"vlan_id" gorm:"column:vlan_id;not null;default:0"`
	NodeID    *string        `json:"node_id,omitempty" gorm:"type:uuid;index"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for ManagedNetwork.
func (ManagedNetwork) TableName() string { return "managed_networks" }

// BeforeCreate hook for ManagedNetwork.
func (n *ManagedNetwork) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	return nil
}
