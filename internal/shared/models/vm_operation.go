package models

import "time"

// VM operation statuses.
const (
	VMOperationRunning   = "running"
	VMOperationCompleted = "completed"
	VMOperationFailed    = "failed"
)

// VMOperation records the progress of a multi-step VM operation (delete, create,
// rebuild) so the UI can show step-by-step progress and the final outcome. The
// row is intentionally NOT tied to the vms row by a foreign key: a delete
// operation must outlive the VM it removes so the final state stays readable.
type VMOperation struct {
	ID          string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID        string     `json:"vm_id" gorm:"type:uuid;not null;index"`
	Operation   string     `json:"operation" gorm:"type:varchar(32);not null"`
	Status      string     `json:"status" gorm:"type:varchar(16);not null;default:running"`
	CurrentStep int        `json:"current_step" gorm:"not null;default:0"`
	TotalSteps  int        `json:"total_steps" gorm:"not null;default:0"`
	StepLabel   string     `json:"step_label" gorm:"type:varchar(200);not null;default:''"`
	Error       string     `json:"error" gorm:"type:text;not null;default:''"`
	StartedAt   time.Time  `json:"started_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TableName specifies the table name for VMOperation.
func (VMOperation) TableName() string { return "vm_operations" }
