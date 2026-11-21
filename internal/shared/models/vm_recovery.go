package models

import "time"

// FeatureVMRecovery is the agent feature for recovering a failed node's VMs:
// the ADOPT command, SetFencedVMs and RemoveFencedVM.
const FeatureVMRecovery = "vm_recovery"

// AdoptImageRef is the image reference of an ADOPT command: the VM's disks
// exist already, so there is no image.
const AdoptImageRef = "adopt:"

// AdoptSeedKey, in an ADOPT command's metadata, asks the node to write the
// VM's cloud-init disc again: the VM was made from a template (it had one)
// and its disks are on RBD, so the disc stayed on the failed node.
const AdoptSeedKey = "adopt_seed"

// How a VM is recovered from a node that went down.
const (
	// RecoveryShared defines the VM on another member of its shared pool, on
	// the same disk files.
	RecoveryShared = "shared"
	// RecoveryBackup restores the VM's latest backup onto another node.
	RecoveryBackup = "backup"
)

// Recovery statuses. Queued and running are active: a VM has at most one.
const (
	RecoveryQueued    = "queued"
	RecoveryRunning   = "running"
	RecoveryCompleted = "completed"
	RecoveryFailed    = "failed"
)

// What became of the VM's copy on the node it was recovered from.
const (
	// OldCopyPending: the old node has not been told yet (it is still down).
	OldCopyPending = "pending"
	// OldCopyFenced: the old node knows; its copy is shut off and will not start.
	OldCopyFenced = "fenced"
	// OldCopyRunning: the old node knows, and found its copy running.
	OldCopyRunning = "running"
	// OldCopyRemoved: nothing of the VM is left on the old node.
	OldCopyRemoved = "removed"
)

// VMRecovery is one recovery of one VM from a failed node. Not tied to the vms
// row by a foreign key: the record outlives the VM.
type VMRecovery struct {
	ID           string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID         string     `json:"vm_id" gorm:"type:uuid;not null"`
	SourceNodeID string     `json:"source_node_id" gorm:"type:uuid;not null"`
	DestNodeID   string     `json:"dest_node_id" gorm:"type:uuid;not null"`
	Method       string     `json:"method" gorm:"type:varchar(16);not null"`
	BackupID     *string    `json:"backup_id,omitempty" gorm:"type:uuid"`
	StartVM      bool       `json:"start_vm" gorm:"column:start_vm;not null"`
	NewAddress   bool       `json:"new_address" gorm:"not null"`
	Status       string     `json:"status" gorm:"type:varchar(16);not null;default:queued"`
	Phase        string     `json:"phase" gorm:"type:varchar(200);not null;default:''"`
	Error        string     `json:"error" gorm:"type:text;not null;default:''"`
	Note         string     `json:"note" gorm:"type:text;not null;default:''"`
	Moved        bool       `json:"moved" gorm:"not null"`
	OldCopy      string     `json:"old_copy" gorm:"type:varchar(16);not null;default:''"`
	RequestedBy  *string    `json:"requested_by,omitempty" gorm:"type:uuid"`
	JobID        *int64     `json:"job_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func (VMRecovery) TableName() string { return "vm_recoveries" }

// Active reports a recovery that has not reached its final state.
func (r *VMRecovery) Active() bool { return r.Status == RecoveryQueued || r.Status == RecoveryRunning }
