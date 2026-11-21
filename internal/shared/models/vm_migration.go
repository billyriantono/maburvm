package models

import "time"

// VM migration statuses. Queued, running and cancelling are active: a VM has
// at most one active migration.
const (
	MigrationQueued     = "queued"
	MigrationRunning    = "running"
	MigrationCancelling = "cancelling"
	MigrationCompleted  = "completed"
	MigrationFailed     = "failed"
	MigrationCancelled  = "cancelled"
	// MigrationSkipped is a VM a drain could not move; Error says why.
	MigrationSkipped = "skipped"
)

// Migration modes.
const (
	MigrationLive    = "live"    // running VM, memory and disks copied while it runs
	MigrationOffline = "offline" // stopped VM, disks copied
)

// VMMigration is one move of one VM between nodes. Not tied to the vms row by
// a foreign key: the record outlives the VM.
type VMMigration struct {
	ID            string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID          string     `json:"vm_id" gorm:"type:uuid;not null"`
	SourceNodeID  string     `json:"source_node_id" gorm:"type:uuid;not null"`
	DestNodeID    *string    `json:"dest_node_id" gorm:"type:uuid"`
	DestPoolID    *string    `json:"dest_pool_id" gorm:"type:uuid"`
	DrainID       *string    `json:"drain_id,omitempty" gorm:"type:uuid"`
	Mode          string     `json:"mode" gorm:"type:varchar(16);not null;default:''"`
	Status        string     `json:"status" gorm:"type:varchar(16);not null;default:queued"`
	Phase         string     `json:"phase" gorm:"type:varchar(200);not null;default:''"`
	BandwidthMbps int        `json:"bandwidth_mbps" gorm:"not null;default:0"`
	BytesTotal    int64      `json:"bytes_total" gorm:"not null;default:0"`
	BytesDone     int64      `json:"bytes_done" gorm:"not null;default:0"`
	Error         string     `json:"error" gorm:"type:text;not null;default:''"`
	Note          string     `json:"note" gorm:"type:text;not null;default:''"`
	RequestedBy   *string    `json:"requested_by,omitempty" gorm:"type:uuid"`
	JobID         *int64     `json:"job_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

func (VMMigration) TableName() string { return "vm_migrations" }

// Active reports a migration that has not reached its final state.
func (m *VMMigration) Active() bool {
	return m.Status == MigrationQueued || m.Status == MigrationRunning || m.Status == MigrationCancelling
}

// Node drain statuses.
const (
	DrainRunning  = "running"
	DrainAborting = "aborting"
	// DrainCompleted: every VM moved. DrainIncomplete: done, but some VMs
	// could not be moved and still run on the node.
	DrainCompleted  = "completed"
	DrainIncomplete = "incomplete"
	DrainAborted    = "aborted"
)

// NodeDrain moves every VM off a node, a few at a time.
type NodeDrain struct {
	ID          string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	NodeID      string `json:"node_id" gorm:"type:uuid;not null"`
	Status      string `json:"status" gorm:"type:varchar(16);not null;default:running"`
	Concurrency int    `json:"concurrency" gorm:"not null;default:1"`
	// Destinations are the operator's choices, VM id to node id; a VM not
	// listed goes to the node it fits best.
	Destinations map[string]string `json:"destinations" gorm:"type:jsonb;serializer:json;not null"`
	RequestedBy  *string           `json:"requested_by,omitempty" gorm:"type:uuid"`
	JobID        *int64            `json:"job_id,omitempty"`
	Error        string            `json:"error" gorm:"type:text;not null;default:''"`
	StartedAt    time.Time         `json:"started_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time         `json:"updated_at" gorm:"not null;default:NOW()"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
}

func (NodeDrain) TableName() string { return "node_drains" }
