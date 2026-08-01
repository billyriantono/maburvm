package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PlatformQuotaCapLifecycle is the lifecycle state of a platform quota-cap
// revision. Revisions are immutable snapshots; only the lifecycle column
// transitions candidate -> active -> retired.
type PlatformQuotaCapLifecycle string

const (
	// PlatformCapCandidate is a staged but not-yet-active cap.
	PlatformCapCandidate PlatformQuotaCapLifecycle = "candidate"
	// PlatformCapActive is the single active cap backing new policy versions and
	// managed assignments.
	PlatformCapActive PlatformQuotaCapLifecycle = "active"
	// PlatformCapRetired is a cap that was superseded or withdrawn.
	PlatformCapRetired PlatformQuotaCapLifecycle = "retired"
)

// PlatformQuotaCapState is the singleton lifecycle state of the active cap
// pointer ('inactive' means no active cap is set).
type PlatformQuotaCapStateLifecycle string

const (
	PlatformCapStateInactive PlatformQuotaCapStateLifecycle = "inactive"
	PlatformCapStateActive   PlatformQuotaCapStateLifecycle = "active"
)

// PlatformQuotaCapRevision is an immutable, administrator-configurable ceiling
// on every account quota-policy version. All dimensions are strictly positive
// and finite. A revision is staged as a candidate, activated (becoming the
// single active cap), and may later be retired. No default cap values exist;
// until an admin publishes and activates one, managed policy/assignment is
// unavailable (fail-closed).
type PlatformQuotaCapRevision struct {
	ID          string                    `json:"id" gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	MaxVMs      int                       `json:"max_vms" gorm:"column:max_vms;not null;check:max_vms > 0"`
	MaxVCPU     int                       `json:"max_vcpu" gorm:"column:max_vcpu;not null;check:max_vcpu > 0"`
	MaxRAMMB    int                       `json:"max_ram_mb" gorm:"column:max_ram_mb;not null;check:max_ram_mb > 0"`
	MaxDiskGB   int                       `json:"max_disk_gb" gorm:"column:max_disk_gb;not null;check:max_disk_gb > 0"`
	State       PlatformQuotaCapLifecycle `json:"state" gorm:"column:state;type:varchar(16);not null;default:'candidate'"`
	Revision    int64                     `json:"revision" gorm:"column:revision;not null;unique"`
	CreatedBy   *string                   `json:"created_by,omitempty" gorm:"column:created_by;type:uuid"`
	Note        string                    `json:"note,omitempty" gorm:"column:note;type:varchar(255)"`
	CreatedAt   time.Time                 `json:"created_at" gorm:"column:created_at;not null;default:NOW()"`
	ActivatedAt *time.Time                `json:"activated_at,omitempty" gorm:"column:activated_at;type:timestamptz"`
	RetiredAt   *time.Time                `json:"retired_at,omitempty" gorm:"column:retired_at;type:timestamptz"`
}

// TableName specifies the table name for PlatformQuotaCapRevision.
func (PlatformQuotaCapRevision) TableName() string { return "platform_quota_cap_revisions" }

// BeforeCreate generates an ID when one was not supplied. The immutable revision
// number is assigned by the repository under an advisory lock, not here.
func (c *PlatformQuotaCapRevision) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return nil
}

// PlatformQuotaCapState is the typed singleton active pointer for the platform
// cap control plane. Exactly one row (singleton_key = 'A') exists. When a cap is
// active, ActiveRevisionID references it and State is 'active'.
type PlatformQuotaCapState struct {
	SingletonKey     string                         `json:"singleton_key" gorm:"column:singleton_key;type:varchar(1);primaryKey;default:'A'"`
	ActiveRevisionID *string                        `json:"active_revision_id,omitempty" gorm:"column:active_revision_id;type:uuid"`
	State            PlatformQuotaCapStateLifecycle `json:"state" gorm:"column:state;type:varchar(16);not null;default:'inactive'"`
	UpdatedBy        *string                        `json:"updated_by,omitempty" gorm:"column:updated_by;type:uuid"`
	UpdatedAt        time.Time                      `json:"updated_at" gorm:"column:updated_at;not null;default:NOW()"`
}

// TableName specifies the table name for PlatformQuotaCapState.
func (PlatformQuotaCapState) TableName() string { return "platform_quota_cap_state" }
