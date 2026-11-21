// Package queue provides PostgreSQL-based job queue using River
package queue

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/maburvm/panel/internal/shared/models"
)

// Job priorities. River accepts 1 (first) to 4 and refuses to queue anything else.
const (
	PriorityCritical = 1 // VM lifecycle operations
	PriorityHigh     = 2 // User-facing operations
	PriorityNormal   = 3 // Background tasks
	PriorityLow      = 4 // Batch operations
)

// Queue names
const (
	QueueCritical = "critical" // VM lifecycle: 20 workers
	QueueDefault  = "default"  // General operations: 50 workers
	QueueBatch    = "batch"    // Backups, imports: 10 workers
	QueueAudit    = "audit"    // Audit logging: 20 workers
)

// VMOperationType represents the type of VM operation
type VMOperationType string

const (
	VMOpCreate            VMOperationType = "create"
	VMOpStart             VMOperationType = "start"
	VMOpStop              VMOperationType = "stop"
	VMOpRestart           VMOperationType = "restart"
	VMOpDelete            VMOperationType = "delete"
	VMOpRebuild           VMOperationType = "rebuild"
	VMOpSuspend           VMOperationType = "suspend"
	VMOpUnsuspend         VMOperationType = "unsuspend"
	VMOpMigrate           VMOperationType = "migrate"
	VMOpResize            VMOperationType = "resize"
	VMOpResetPassword     VMOperationType = "reset_password"
	VMOpAttachISO         VMOperationType = "attach_iso"
	VMOpDetachISO         VMOperationType = "detach_iso"
	VMOpConfigureNetwork VMOperationType = "configure_network"
)

// ISOSourceOperator is the "iso_source" param of an attach-ISO job whose image
// the operator chose. A job without it carries a URL a customer typed.
const ISOSourceOperator = "operator"

// VMOperationJob represents a VM lifecycle operation job
// This is inserted into the critical queue
type VMOperationJob struct {
	VMID      string          `json:"vm_id" river:"unique"`
	Operation VMOperationType `json:"operation" river:"unique"`
	NodeID    string          `json:"node_id"`
	// In the unique key, so a reset or reinstall with new values is not folded into an older one.
	Params json.RawMessage `json:"params,omitempty" river:"unique"`
}

// Kind returns the job kind for VM operations
func (j VMOperationJob) Kind() string {
	return "vm_operation"
}

// InsertOpts returns insert options for VM operations (critical queue)
func (j VMOperationJob) InsertOpts() river.InsertOpts {
	// Network config is never de-duplicated: each job means "the desired state
	// changed", and skipping one behind a job that is already running loses
	// that change. The worker serialises them per VM and reads current rules.
	if j.Operation == VMOpConfigureNetwork {
		return river.InsertOpts{Queue: QueueCritical, Priority: PriorityCritical, MaxAttempts: NetworkJobMaxAttempts}
	}
	return river.InsertOpts{
		Queue:       QueueCritical,
		Priority:    PriorityCritical,
		MaxAttempts: NodeJobMaxAttempts,
		// Idempotent enqueue: a second insert of the same (vm_id, operation,
		// params) while one is still in-flight is skipped instead of duplicating
		// the operation. Deliberately excludes Completed/
		// Discarded/Cancelled so a repeat op after the previous one finished
		// (start→stop→start, or a re-delete) is still allowed.
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// TemplateSyncJob represents a template synchronization job
// Distributes OS templates to KVM nodes
// This is inserted into the default queue
type TemplateSyncJob struct {
	TemplateID string   `json:"template_id"`
	NodeIDs    []string `json:"node_ids"`
	Force      bool     `json:"force"` // Force re-sync even if template exists
}

// Kind returns the job kind for template sync
func (j TemplateSyncJob) Kind() string {
	return "template_sync"
}

// InsertOpts returns insert options for template sync (default queue)
func (j TemplateSyncJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueDefault,
		Priority:    PriorityNormal,
		MaxAttempts: NodeJobMaxAttempts,
	}
}

// BackupType represents the type of backup
type BackupType string

const (
	BackupTypeSnapshot BackupType = "snapshot"
	BackupTypeFull     BackupType = "full"
)

// BackupJob represents a VM backup job
// This is inserted into the batch queue
type BackupJob struct {
	VMID            string     `json:"vm_id"`
	BackupType      BackupType `json:"backup_type"`
	StorageProvider string     `json:"storage_provider"` // e.g., "s3", "local"
	Destination     string     `json:"destination"`      // S3 bucket or local path
	Compression     string     `json:"compression"`      // gzip, zstd, none
	BackupID        string     `json:"backup_id"`        // Reference to backup record
	// Scheduled backups start only inside the backup window.
	Scheduled bool `json:"scheduled,omitempty"`
}

// Kind returns the job kind for backup
func (j BackupJob) Kind() string {
	return "backup"
}

// ImageJob captures a VM's disk to a standalone object-storage image (reusing
// the agent BackupDisk export). It writes back to an images row on completion.
type ImageJob struct {
	ImageID     string `json:"image_id"`
	VMID        string `json:"vm_id"`
	Destination string `json:"destination"` // object-storage key (images/<id>.qcow2)
}

// Kind returns the job kind for image capture.
func (j ImageJob) Kind() string { return "image" }

// InsertOpts routes image capture to the batch queue with the same
// long-transfer characteristics as backups.
func (j ImageJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueBatch, Priority: PriorityLow, MaxAttempts: DiskJobMaxAttempts}
}

// DiskJobMaxAttempts is how many times a backup, a restore or an image capture
// is tried. Each attempt moves a whole disk, so River's default of 25 is not
// persistence, it is 25 exports of the same disk.
const DiskJobMaxAttempts = 3

// River's default of 25 tries spreads retries over about three weeks, and the
// operator hears of a failure only after the last one. Retries are spaced
// attempt^4 seconds apart.
const (
	// NodeJobMaxAttempts covers VM lifecycle, snapshot, template and import
	// jobs: about six minutes of retries.
	NodeJobMaxAttempts = 5
	// NetworkJobMaxAttempts is longer (about four hours): a network change is
	// desired state that must reach a node that is briefly away.
	NetworkJobMaxAttempts = 10
)

// InsertOpts returns insert options for backup (batch queue)
func (j BackupJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueBatch,
		Priority:    PriorityLow,
		MaxAttempts: DiskJobMaxAttempts,
	}
}

// RestoreJob restores a VM's primary disk from a completed backup.
// Runs on the critical queue (data-affecting VM lifecycle op).
type RestoreJob struct {
	VMID      string `json:"vm_id"`
	NodeID    string `json:"node_id"`
	BackupID  string `json:"backup_id"`
	SourceKey string `json:"source_key"`
	Checksum  string `json:"checksum"`
	// NodeStore is true for a backup kept in the node's own storage: the node
	// is not sent the panel's destination, where the file is not.
	NodeStore bool `json:"node_store,omitempty"`
	// Disks are every disk of a backup of several; each is restored onto the
	// VM's disk with the same target. Empty for a backup of the primary only.
	Disks []models.BackupDisk `json:"disks,omitempty"`
}

func (j RestoreJob) Kind() string { return "restore" }

func (j RestoreJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueCritical, Priority: PriorityCritical, MaxAttempts: DiskJobMaxAttempts}
}

// ImportSource represents the source of import
type ImportSource string

const (
	ImportSourceVirtualizor ImportSource = "virtualizor"
	ImportSourceManual      ImportSource = "manual"
)

// ImportJob represents a VM import job (e.g., from another platform)
// This is inserted into the batch queue
type ImportJob struct {
	Source     ImportSource    `json:"source"`
	SourceID   string          `json:"source_id"`   // Original VM ID in source system
	NodeID     string          `json:"node_id"`     // Target node
	UserID     string          `json:"user_id"`     // Owner user ID
	ConfigPath string          `json:"config_path"` // Path to XML config (for imported guests)
	DiskPath   string          `json:"disk_path"`   // Path to disk image
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// Kind returns the job kind for import
func (j ImportJob) Kind() string {
	return "import"
}

// InsertOpts returns insert options for import (batch queue)
func (j ImportJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueBatch,
		Priority:    PriorityLow,
		MaxAttempts: NodeJobMaxAttempts,
	}
}

// SnapshotOperation represents the type of snapshot operation
type SnapshotOperation string

const (
	SnapshotOpCreate  SnapshotOperation = "create"
	SnapshotOpRestore SnapshotOperation = "restore"
	SnapshotOpDelete  SnapshotOperation = "delete"
)

// SnapshotJob represents a VM snapshot operation job
// This is inserted into the critical queue
type SnapshotJob struct {
	VMID       string            `json:"vm_id"`
	SnapshotID string            `json:"snapshot_id"`
	Operation  SnapshotOperation `json:"operation"`
	NodeID     string            `json:"node_id"`
	Name       string            `json:"name,omitempty"`
	DiskPath   string            `json:"disk_path,omitempty"`
	Params     json.RawMessage   `json:"params,omitempty"`
}

// Kind returns the job kind for snapshot operations
func (j SnapshotJob) Kind() string {
	return "snapshot_operation"
}

// InsertOpts returns insert options for snapshot operations (critical queue)
func (j SnapshotJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueCritical,
		Priority:    PriorityHigh,
		MaxAttempts: NodeJobMaxAttempts,
	}
}

// ValidateSnapshotOperation validates snapshot operation type
func ValidateSnapshotOperation(op SnapshotOperation) error {
	switch op {
	case SnapshotOpCreate, SnapshotOpRestore, SnapshotOpDelete:
		return nil
	default:
		return fmt.Errorf("invalid snapshot operation type: %s", op)
	}
}

// ValidateVMOperation validates VM operation parameters
func ValidateVMOperation(op VMOperationType) error {
	switch op {
	case VMOpCreate, VMOpStart, VMOpStop, VMOpRestart, VMOpDelete,
		VMOpRebuild, VMOpSuspend, VMOpUnsuspend, VMOpMigrate, VMOpResize, VMOpResetPassword,
		VMOpAttachISO, VMOpDetachISO,
		VMOpConfigureNetwork:
		return nil
	default:
		return fmt.Errorf("invalid VM operation type: %s", op)
	}
}

// ValidateBackupType validates backup type
func ValidateBackupType(bt BackupType) error {
	switch bt {
	case BackupTypeSnapshot, BackupTypeFull:
		return nil
	default:
		return fmt.Errorf("invalid backup type: %s", bt)
	}
}

// ValidateImportSource validates import source
func ValidateImportSource(src ImportSource) error {
	switch src {
	case ImportSourceVirtualizor, ImportSourceManual:
		return nil
	default:
		return fmt.Errorf("invalid import source: %s", src)
	}
}

type AuditJob struct {
	UserID         *string         `json:"user_id,omitempty"`
	Action         string          `json:"action"`
	ResourceType   string          `json:"resource_type,omitempty"`
	ResourceID     *string         `json:"resource_id,omitempty"`
	IPAddress      string          `json:"ip_address,omitempty"`
	UserAgent      string          `json:"user_agent,omitempty"`
	Details        map[string]any  `json:"details,omitempty"`
	BeforeSnapshot *map[string]any `json:"before_snapshot,omitempty"`
	AfterSnapshot  *map[string]any `json:"after_snapshot,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
}

func (j AuditJob) Kind() string {
	return "audit"
}

func (j AuditJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:    QueueAudit,
		Priority: PriorityNormal,
	}
}
