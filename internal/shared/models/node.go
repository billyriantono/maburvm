package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NodeStatus represents the status of a node
type NodeStatus string

const (
	NodeStatusActive      NodeStatus = "active"
	NodeStatusMaintenance NodeStatus = "maintenance"
	NodeStatusOffline     NodeStatus = "offline"
)

// Node represents a compute node in the system
type Node struct {
	ID        string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string     `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	IPAddress string     `json:"ip_address" gorm:"type:inet;not null" validate:"required,ip"`
	Status    NodeStatus `json:"status" gorm:"type:node_status;default:offline" validate:"required,oneof=active maintenance offline"`
	Token     string     `json:"-" gorm:"type:varchar(255);uniqueIndex;not null"` // Never exposed in JSON
	// CertFingerprint is the pinned SHA-256 fingerprint of the agent's TLS cert,
	// recorded on first connection (trust on first use) and verified thereafter.
	// Empty = not yet pinned. Never exposed in JSON.
	CertFingerprint string `json:"-" gorm:"type:varchar(128);not null;default:''"`
	// RegionID is the location customers see and choose. Nullable so nodes
	// registered before regions existed keep working until an operator assigns
	// them.
	RegionID *string `json:"region_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	// Zone is stored but deliberately NOT exposed to customers yet: an
	// availability zone implies a VPC that spans it, a floating IP that crosses
	// it, and failover between zones — none of which exist. Kept so introducing
	// zones later needs no migration.
	Zone      string         `json:"-" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Node
func (Node) TableName() string {
	return "nodes"
}

// BeforeCreate hook for Node
func (n *Node) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	if n.Status == "" {
		n.Status = NodeStatusOffline
	}
	return nil
}

// GetID returns the UUID representation of ID
func (n *Node) GetID() uuid.UUID {
	id, _ := uuid.Parse(n.ID)
	return id
}

// Validate validates the Node struct
func (n *Node) Validate() ValidationErrors {
	return ValidateStruct(n)
}
