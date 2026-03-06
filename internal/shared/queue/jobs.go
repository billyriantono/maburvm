// Package queue provides PostgreSQL-based job queue using River
package queue

import (
	"encoding/json"
	"fmt"

	"github.com/riverqueue/river"
)

// Job priorities
const (
	PriorityCritical = 1 // VM lifecycle operations
	PriorityHigh     = 2 // User-facing operations
	PriorityNormal   = 4 // Background tasks
	PriorityLow      = 8 // Batch operations
)

// Queue names
const (
	QueueCritical = "critical" // VM lifecycle: 20 workers
	QueueDefault  = "default"  // General operations: 50 workers
	QueueBatch    = "batch"    // Backups, imports: 10 workers
)

// VMOperationType represents the type of VM operation
type VMOperationType string

const (
	VMOpCreate    VMOperationType = "create"
	VMOpStart     VMOperationType = "start"
	VMOpStop      VMOperationType = "stop"
	VMOpRestart   VMOperationType = "restart"
	VMOpDelete    VMOperationType = "delete"
	VMOpRebuild   VMOperationType = "rebuild"
	VMOpSuspend   VMOperationType = "suspend"
	VMOpUnsuspend VMOperationType = "unsuspend"
	VMOpMigrate   VMOperationType = "migrate"
	VMOpResize    VMOperationType = "resize"
)

// VMOperationJob represents a VM lifecycle operation job
// This is inserted into the critical queue
type VMOperationJob struct {
	VMID      string          `json:"vm_id"`
	Operation VMOperationType `json:"operation"`
	NodeID    string          `json:"node_id"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// Kind returns the job kind for VM operations
func (j VMOperationJob) Kind() string {
	return "vm_operation"
}

// InsertOpts returns insert options for VM operations (critical queue)
func (j VMOperationJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:    QueueCritical,
		Priority: PriorityCritical,
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
func (j TemplateSyncJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:    QueueDefault,
		Priority: PriorityNormal,
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
}

// Kind returns the job kind for backup
func (j BackupJob) Kind() string {
	return "backup"
}

// InsertOpts returns insert options for backup (batch queue)
func (j BackupJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:    QueueBatch,
		Priority: PriorityLow,
	}
}

// ImportSource represents the source of import
type ImportSource string

const (
	ImportSourceVirtualizor ImportSource = "virtualizor"
	ImportSourceManual      ImportSource = "manual"
)

// ImportJob represents a VM import job (e.g., from Virtualizor)
// This is inserted into the batch queue
type ImportJob struct {
	Source     ImportSource    `json:"source"`
	SourceID   string          `json:"source_id"`   // Original VM ID in source system
	NodeID     string          `json:"node_id"`     // Target node
	UserID     string          `json:"user_id"`     // Owner user ID
	ConfigPath string          `json:"config_path"` // Path to XML config (for Virtualizor)
	DiskPath   string          `json:"disk_path"`   // Path to disk image
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// Kind returns the job kind for import
func (j ImportJob) Kind() string {
	return "import"
}

// InsertOpts returns insert options for import (batch queue)
func (j ImportJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:    QueueBatch,
		Priority: PriorityLow,
	}
}

// ValidateVMOperation validates VM operation parameters
func ValidateVMOperation(op VMOperationType) error {
	switch op {
	case VMOpCreate, VMOpStart, VMOpStop, VMOpRestart, VMOpDelete,
		VMOpRebuild, VMOpSuspend, VMOpUnsuspend, VMOpMigrate, VMOpResize:
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
