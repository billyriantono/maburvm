package models

import "time"

// QuotaMode distinguishes how a user's effective quota is interpreted.
//
// Phase 1A introduces managed account quota policies. Existing accounts and
// their user_quotas rows predate this feature and must keep behaving exactly as
// before: a zero limit continues to mean unlimited. New enrollments will instead
// be governed by an immutable quota policy version selected by an admin; under
// the managed mode a zero/empty snapshot still means "no limit was assigned"
// but it is interpreted by the (not-yet-built) Phase 1B service which decides
// whether enrollment is permitted.
//
// The marker is additive and defaults to legacy for every existing row, so
// behavior is unchanged until an admin opts an account into managed policy
// enforcement.
type QuotaMode string

const (
	// QuotaModeLegacy is the default for all existing/imported accounts.
	// Zero limits mean unlimited, exactly as before Phase 1A.
	QuotaModeLegacy QuotaMode = "legacy"
	// QuotaModeManaged means the row's snapshot derives from (or may later be
	// overridden against) a named immutable quota policy version.
	QuotaModeManaged QuotaMode = "managed"
)

// UserQuota caps the resources a user may allocate across all their VMs.
// A zero value for any limit means "unlimited" for that dimension when the
// quota mode is legacy. For managed accounts the limits are a snapshot copied
// from the selected immutable quota policy version; see QuotaMode.
type UserQuota struct {
	// Explicit column names are required: GORM's default naming would turn
	// MaxVCPU into "max_v_cpu", which would not match migration 012 ("max_vcpu").
	UserID    string    `json:"user_id" gorm:"column:user_id;type:uuid;primaryKey"`
	MaxVMs    int       `json:"max_vms" gorm:"column:max_vms;not null;default:0"`         // max number of VMs
	MaxVCPU   int       `json:"max_vcpu" gorm:"column:max_vcpu;not null;default:0"`       // total vCPUs across VMs
	MaxRAMMB  int       `json:"max_ram_mb" gorm:"column:max_ram_mb;not null;default:0"`   // total RAM in MB
	MaxDiskGB int       `json:"max_disk_gb" gorm:"column:max_disk_gb;not null;default:0"` // total disk in GB
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at;not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at;not null;default:NOW()"`

	// QuotaMode marks whether this row is a legacy account (zero = unlimited) or
	// a managed account governed by a selected quota policy version. Defaults to
	// legacy for every existing row via migration 033.
	QuotaMode QuotaMode `json:"quota_mode" gorm:"column:quota_mode;type:quota_mode;not null;default:'legacy'"`

	// Provenance of the snapshot. For managed accounts this points at the exact
	// immutable policy version that was copied into the limits above. Null for
	// legacy accounts and for managed accounts whose limits were set/overridden
	// directly by an admin without referencing a policy version.
	//
	// A managed quota row is only considered usable when ALL of the following are
	// populated: PolicyID, PolicyVersion, PolicyName, PolicyAssignedAt,
	// CapRevisionID, AND all four limits are strictly positive. PolicyName and
	// PolicyAssignedAt are part of the complete provenance because a row without
	// them cannot be audited back to the assignment event. See managedQuotaUsable.
	//
	// IMPORTANT: a higher (or merely newer) active cap does NOT authorize reusing
	// an older cap-bound policy version. If the active platform cap advances, a
	// NEW policy version bound to the new cap_revision_id must be published before
	// a managed assignment is allowed; the old version remains tied to the old cap.
	PolicyID         *string    `json:"policy_id,omitempty" gorm:"column:policy_id;type:uuid"`
	PolicyVersion    *int       `json:"policy_version,omitempty" gorm:"column:policy_version;type:integer"`
	PolicyName       *string    `json:"policy_name,omitempty" gorm:"column:policy_name;type:varchar(100)"`
	PolicyAssignedAt *time.Time `json:"policy_assigned_at,omitempty" gorm:"column:policy_assigned_at;type:timestamptz"`
	PolicyAssignedBy *string    `json:"policy_assigned_by,omitempty" gorm:"column:policy_assigned_by;type:uuid"`
	// CapRevisionID records the active platform quota-cap revision that
	// authorized this snapshot. Null for legacy rows and for any managed row that
	// was not produced through the cap-aware assignment path. It is the binding
	// link that ties a policy version to the cap under which it was valid; the
	// parallel SQL lane (migration 039) enforces exact equality between a policy
	// version's cap_revision_id and the active cap at assignment time.
	CapRevisionID *string `json:"cap_revision_id,omitempty" gorm:"column:cap_revision_id;type:uuid"`
}

// TableName specifies the table name for UserQuota.
func (UserQuota) TableName() string { return "user_quotas" }

// IsManaged reports whether this quota row is governed by the managed policy
// path. Legacy rows (the default for all pre-existing accounts) keep the
// original behavior where a zero limit means unlimited.
func (q UserQuota) IsManaged() bool { return q.QuotaMode == QuotaModeManaged }

// HasPolicyProvenance reports whether this row's snapshot can be traced back to
// a specific immutable quota policy version.
func (q UserQuota) HasPolicyProvenance() bool {
	return q.PolicyID != nil && q.PolicyVersion != nil
}
