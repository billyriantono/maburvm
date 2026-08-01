package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// QuotaPolicyLifecycle is the mutable lifecycle state of a named quota policy.
type QuotaPolicyLifecycle string

const (
	// QuotaPolicyActive policies may be selected as the account default.
	// At most one active policy may carry the is_default flag (enforced by a
	// partial unique index in migration 033). A policy may be deprecated but
	// must never be physically deleted so already-assigned snapshots remain
	// meaningful.
	QuotaPolicyActive QuotaPolicyLifecycle = "active"
	// QuotaPolicyDeprecated policies can no longer be assigned to new accounts
	// but existing assignments keep their immutable version snapshot.
	QuotaPolicyDeprecated QuotaPolicyLifecycle = "deprecated"
)

// QuotaPolicy is a named, versioned account quota policy. Individual limits are
// NOT stored on the policy itself; they live on immutable QuotaPolicyVersion
// rows so a published limit set can never be silently changed after an account
// has been enrolled against it.
type QuotaPolicy struct {
	ID          string               `json:"id" gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name        string               `json:"name" gorm:"column:name;type:varchar(100);not null;uniqueIndex"`
	Description string               `json:"description,omitempty" gorm:"column:description;type:text"`
	Lifecycle   QuotaPolicyLifecycle `json:"lifecycle" gorm:"column:lifecycle;type:quota_policy_lifecycle;not null;default:'active'"`
	// IsDefault marks the single active policy that new enrollments fall back to
	// when an explicit policy is not chosen. Exactly one active default may exist
	// (partial unique index on (is_default) WHERE is_default = true AND lifecycle = 'active').
	IsDefault bool                 `json:"is_default" gorm:"column:is_default;not null;default:false"`
	CreatedAt time.Time            `json:"created_at" gorm:"column:created_at;not null;default:NOW()"`
	UpdatedAt time.Time            `json:"updated_at" gorm:"column:updated_at;not null;default:NOW()"`
	Versions  []QuotaPolicyVersion `json:"versions,omitempty" gorm:"foreignKey:PolicyID;references:ID;constraint:OnDelete:RESTRICT"`
}

// TableName specifies the table name for QuotaPolicy.
func (QuotaPolicy) TableName() string { return "quota_policies" }

// QuotaPolicyVersion is an immutable, append-only set of account limits for a
// named policy. Versions are never updated or deleted; a changed limit set is
// published as a new version with the next higher version number.
type QuotaPolicyVersion struct {
	ID        string `json:"id" gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	PolicyID  string `json:"policy_id" gorm:"column:policy_id;type:uuid;not null;index"`
	Version   int    `json:"version" gorm:"column:version;not null"`
	MaxVMs    int    `json:"max_vms" gorm:"column:max_vms;not null;check:max_vms > 0"`
	MaxVCPU   int    `json:"max_vcpu" gorm:"column:max_vcpu;not null;check:max_vcpu > 0"`
	MaxRAMMB  int    `json:"max_ram_mb" gorm:"column:max_ram_mb;not null;check:max_ram_mb > 0"`
	MaxDiskGB int    `json:"max_disk_gb" gorm:"column:max_disk_gb;not null;check:max_disk_gb > 0"`
	// CapRevisionID binds the version to the active platform quota-cap revision
	// that authorized it. Null for pre-037 versions, which remain readable but
	// cannot be used for managed assignment under the new provenance contract.
	CapRevisionID *string   `json:"cap_revision_id,omitempty" gorm:"column:cap_revision_id;type:uuid"`
	Note          string    `json:"note,omitempty" gorm:"column:note;type:varchar(255)"`
	CreatedAt     time.Time `json:"created_at" gorm:"column:created_at;not null;default:NOW()"`
}

// TableName specifies the table name for QuotaPolicyVersion.
func (QuotaPolicyVersion) TableName() string { return "quota_policy_versions" }

// BeforeCreate enforces the unique (policy_id, version) tuple and generates an
// ID when one was not supplied. Physical deletion is blocked at the repository
// layer, not here.
func (v *QuotaPolicyVersion) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	return nil
}

// BeforeCreate generates an ID when one was not supplied for the policy.
func (p *QuotaPolicy) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}
