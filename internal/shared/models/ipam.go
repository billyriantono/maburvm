package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	IPFamilyIPv4 = "ipv4"
	IPFamilyIPv6 = "ipv6"

	IPAddressStatusAvailable = "available"
	IPAddressStatusReserved  = "reserved"
	IPAddressStatusAssigned  = "assigned"
	IPAddressStatusDisabled  = "disabled"

	// Delivery mode: how the address reaches the VM.
	// Direct = bridged and bound inside the guest (the pre-existing model).
	// Floating = configured on the host and NATed to the VM's own address, so it
	// can be moved between VMs on the node without touching either guest.
	IPDeliveryDirect   = "direct"
	IPDeliveryFloating = "floating"

	// NAT mode for a floating IP.
	// Inbound = DNAT only; conntrack reverses it for replies, and the VM still
	// egresses under its own identity (baseline masquerade or its own public IP).
	// Full = DNAT + SNAT; the VM egresses *as* the floating IP. Only one full-mode
	// floating IP per VM makes sense, since it overrides the egress identity.
	NATModeInbound = "inbound"
	NATModeFull    = "full"
)

// IPPool represents a first-class IPAM pool. It intentionally does not replace
// the VM-attached networks table.
type IPPool struct {
	ID          string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name        string         `json:"name" gorm:"type:varchar(255);not null" validate:"required"`
	NodeID      *string        `json:"node_id,omitempty" gorm:"type:uuid" validate:"omitempty,uuid"`
	Family      string         `json:"family" gorm:"type:varchar(8);not null;default:'ipv4'" validate:"required,oneof=ipv4 ipv6"`
	CIDR        string         `json:"cidr,omitempty" gorm:"column:cidr;type:cidr" validate:"omitempty,cidr"`
	Gateway     string         `json:"gateway,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	Bridge      string         `json:"bridge,omitempty" gorm:"type:varchar(64)" validate:"omitempty,max=64"`
	RangeStart  string         `json:"range_start,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	RangeEnd    string         `json:"range_end,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	Description string         `json:"description,omitempty" gorm:"type:text"`
	CreatedAt   time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`

	// Many-to-many: loaded separately via ip_pool_nodes junction table
	// When junction table exists, this takes precedence over NodeID
	NodeIDs []string `json:"node_ids" gorm:"-"`
}

func (IPPool) TableName() string { return "ip_pools" }

func (p *IPPool) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.Family == "" {
		p.Family = IPFamilyIPv4
	}
	return nil
}

func (p *IPPool) Validate() ValidationErrors { return ValidateStruct(p) }

// IPAddress represents a managed address in an IPAM pool.
type IPAddress struct {
	ID      string  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	PoolID  string  `json:"pool_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	NodeID  *string `json:"node_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	Address string  `json:"address" gorm:"type:inet;not null" validate:"required,ip"`
	Family  string  `json:"family" gorm:"type:varchar(8);not null;default:'ipv4'" validate:"required,oneof=ipv4 ipv6"`
	Status  string  `json:"status" gorm:"type:varchar(16);not null;default:'available';index" validate:"required,oneof=available reserved assigned disabled"`
	VMID    *string `json:"vm_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	// DeliveryMode/NATMode describe floating IPs (see the IPDelivery* constants).
	// A direct address keeps NATMode empty. UserID records the tenant that owns a
	// floating IP while it is attached to no VM — a floating IP deliberately
	// survives deletion of the VM it was attached to.
	DeliveryMode string         `json:"delivery_mode" gorm:"type:varchar(16);not null;default:'direct'" validate:"omitempty,oneof=direct floating"`
	NATMode      string         `json:"nat_mode,omitempty" gorm:"type:varchar(16);not null;default:''" validate:"omitempty,oneof=inbound full"`
	UserID       *string        `json:"user_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	Note         string         `json:"note,omitempty" gorm:"type:text"`
	RDNS         string         `json:"rdns,omitempty" gorm:"column:rdns;type:varchar(253)"` // reverse DNS (PTR) hostname
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

func (IPAddress) TableName() string { return "ip_addresses" }

func (a *IPAddress) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	if a.Family == "" {
		a.Family = IPFamilyIPv4
	}
	if a.Status == "" {
		a.Status = IPAddressStatusAvailable
	}
	if a.DeliveryMode == "" {
		a.DeliveryMode = IPDeliveryDirect
	}
	return nil
}

func (a *IPAddress) Validate() ValidationErrors { return ValidateStruct(a) }
