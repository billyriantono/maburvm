package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Network represents a network configuration for a VM
type Network struct {
	ID               string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID             string `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	IPAddress        string `json:"ip_address" gorm:"type:inet;not null" validate:"required,ip"`
	BandwidthLimit   int64  `json:"bandwidth_limit" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	BandwidthQuotaGB int64  `json:"bandwidth_quota_gb" gorm:"type:bigint;default:0"` // 0 = unlimited
	// Over-quota enforcement snapshot inherited from the plan at create time, plus
	// a runtime flag so a throttled VM is restored to BandwidthLimit when its
	// quota resets. OverQuotaPolicy is one of models.OverQuota* (throttle|overage|suspend).
	OverQuotaPolicy   string         `json:"over_quota_policy" gorm:"type:varchar(20);default:'throttle'"`
	ThrottleSpeedMbps int            `json:"throttle_speed_mbps" gorm:"default:0"`
	Throttled         bool           `json:"throttled" gorm:"default:false"`
	VLANID            *int           `json:"vlan_id" gorm:"type:integer" validate:"omitempty,min=1,max=4094"`
	AntiSpoofing      bool           `json:"anti_spoofing" gorm:"type:boolean;default:true"` // Enable anti-IP hijacking protection
	CreatedAt         time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt         time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt         gorm.DeletedAt `json:"-" gorm:"index"`
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

// PortForward represents a port forwarding (NAT) rule for a VM
// Maps external host ports to internal VM ports
type PortForward struct {
	ID           string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID         string         `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	NetworkID    string         `json:"network_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	ExternalPort int            `json:"external_port" gorm:"type:integer;not null" validate:"required,min=1,max=65535"`
	InternalPort int            `json:"internal_port" gorm:"type:integer;not null" validate:"required,min=1,max=65535"`
	Protocol     string         `json:"protocol" gorm:"type:varchar(10);default:'tcp'" validate:"omitempty,oneof=tcp udp"`
	SourceIP     string         `json:"source_ip" gorm:"type:cidr;default:'0.0.0.0/0'" validate:"omitempty,ip_or_cidr"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for PortForward
func (PortForward) TableName() string {
	return "port_forwards"
}

// BeforeCreate hook for PortForward
func (p *PortForward) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.Protocol == "" {
		p.Protocol = "tcp"
	}
	return nil
}

// GetID returns the UUID representation of ID
func (p *PortForward) GetID() uuid.UUID {
	id, _ := uuid.Parse(p.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (p *PortForward) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(p.VMID)
	return id
}

// GetNetworkID returns the UUID representation of NetworkID
func (p *PortForward) GetNetworkID() uuid.UUID {
	id, _ := uuid.Parse(p.NetworkID)
	return id
}

// Validate validates the PortForward struct
func (p *PortForward) Validate() ValidationErrors {
	return ValidateStruct(p)
}
