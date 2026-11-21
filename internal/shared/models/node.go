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
	// Token is the agent credential, encrypted at rest (serializer:secret).
	// Never exposed in JSON.
	Token string `json:"-" gorm:"type:varchar(255);uniqueIndex;not null;serializer:secret"`
	// CreationPaused keeps new VMs off the node; the VMs already on it are not
	// affected. Read-only here so a full-row save cannot overwrite it; it is
	// written by table name in NodeService.UpdateNode.
	CreationPaused bool `json:"creation_paused" gorm:"column:creation_paused;->"`
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
	Zone string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// MemoryTotalBytes is the node's physical RAM, refreshed by the metrics
	// collector. Only overcommit admission reads it. 0 means "not measured
	// yet" and is treated as unlimited, so a node the panel has not polled
	// yet keeps provisioning rather than failing closed.
	MemoryTotalBytes int64 `json:"memory_total_bytes" gorm:"not null;default:0"`
	// CPUCount is the node's logical CPUs as its agent last reported them (0 =
	// not measured, treated as unlimited). VCPURatio is this node's own vCPU
	// overcommit ratio; 0 uses the panel-wide one. Read-only here: the collector
	// and NodeService.UpdateNode write them by table name.
	CPUCount  int     `json:"cpu_count" gorm:"column:cpu_count;->"`
	VCPURatio float64 `json:"vcpu_ratio" gorm:"column:vcpu_ratio;->"`
	// NodeGroup is the hypervisor group the node belongs to ("" for none); a
	// plan naming a group is placed only on its nodes. Read-only here,
	// written by table name in NodeService.UpdateNode.
	NodeGroup string `json:"node_group" gorm:"column:node_group;->"`
	// LastReportAt is when the node's agent last answered the collector with
	// real figures; FailedPolls is how many checks in a row it has missed since.
	// Read-only here, like CreationPaused: the collector writes them by table name.
	LastReportAt *time.Time     `json:"last_report_at" gorm:"column:last_report_at;->"`
	FailedPolls  int            `json:"failed_polls" gorm:"column:failed_polls;->"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

// Firewall enforcement of a node, kept in nodes.firewall_enforcement by the
// metrics collector. The column is deliberately not a field of Node: it is
// written and read by table name, so a full-row save cannot overwrite it.
const (
	FirewallEnforced = "enforced"
	FirewallOff      = "off"
	// FirewallUnknown is a node that has not said: unreachable, or running an
	// agent older than the report. Stored as ''.
	FirewallUnknown = "unknown"
)

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
