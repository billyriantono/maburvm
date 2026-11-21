package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Plan network types.
const (
	PlanNetworkPublic = "public"
	// PlanNetworkNAT gives each VM a private address and a block of public
	// ports on its node instead of a public address.
	PlanNetworkNAT = "nat"
)

// NATPortBlock is the contiguous range of public ports a NAT VM holds on its
// node's publish address.
type NATPortBlock struct {
	ID        string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string    `json:"vm_id" gorm:"type:uuid;not null"`
	NodeID    string    `json:"node_id" gorm:"type:uuid;not null"`
	StartPort int       `json:"start_port" gorm:"not null"`
	PortCount int       `json:"port_count" gorm:"not null"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
}

func (NATPortBlock) TableName() string { return "nat_port_blocks" }

func (b *NATPortBlock) BeforeCreate(tx *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	return nil
}

// EndPort is the block's last port.
func (b *NATPortBlock) EndPort() int { return b.StartPort + b.PortCount - 1 }

// Contains reports whether port is in the block.
func (b *NATPortBlock) Contains(port int) bool { return port >= b.StartPort && port <= b.EndPort() }

// VMHostname is a hostname routed to a NAT VM through its node's ports 80 and
// 443. Only a verified one is routed.
type VMHostname struct {
	ID          string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID        string     `json:"vm_id" gorm:"type:uuid;not null"`
	Hostname    string     `json:"hostname" gorm:"type:varchar(253);not null"`
	HTTPPort    int        `json:"http_port" gorm:"column:http_port;not null;default:80"`
	HTTPSPort   int        `json:"https_port" gorm:"column:https_port;not null;default:443"`
	VerifyToken string     `json:"verify_token" gorm:"type:varchar(64);not null"`
	VerifiedAt  *time.Time `json:"verified_at"`
	// NodeError is why the node is not serving the hostname, as it last said.
	NodeError string    `json:"node_error" gorm:"type:text;not null;default:''"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
}

func (VMHostname) TableName() string { return "vm_hostnames" }

func (h *VMHostname) BeforeCreate(tx *gorm.DB) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}
	return nil
}
