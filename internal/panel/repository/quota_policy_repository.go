package repository

import (
	"context"
	"errors"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrQuotaPolicyNotFound is returned when a policy or version does not exist.
var ErrQuotaPolicyNotFound = errors.New("quota policy not found")

// ErrDuplicateQuotaPolicyName is returned when creating a policy whose name
// already exists.
var ErrDuplicateQuotaPolicyName = errors.New("quota policy name already exists")

// ErrDuplicatePolicyVersion is returned when appending a version whose number
// already exists for the policy.
var ErrDuplicatePolicyVersion = errors.New("quota policy version already exists")

// ErrMultipleDefaultQuotaPolicy is returned when an operation would create a
// second active default policy.
var ErrMultipleDefaultQuotaPolicy = errors.New("only one active default quota policy may exist")

// ErrQuotaPolicyHasNoVersion is returned when an operation requires a published
// immutable version (e.g. making a policy the default) but the policy has none.
var ErrQuotaPolicyHasNoVersion = errors.New("quota policy has no published version")

// ErrQuotaPolicyNotActive is returned when an operation requires an active
// policy (e.g. appending a new immutable version) but the policy is deprecated
// or otherwise non-active.
var ErrQuotaPolicyNotActive = errors.New("quota policy is not active")

// ErrNoActiveQuotaCap is returned when an operation requires an active platform
// quota-cap (publishing a policy version or assigning a managed user quota) but
// none is currently active. This is the fail-closed default: managed paths are
// unavailable until an admin publishes and activates a cap. Existing legacy
// users are unaffected.
var ErrNoActiveQuotaCap = errors.New("no active platform quota cap")

// ErrQuotaCapNotFound is returned when a cap revision does not exist.
var ErrQuotaCapNotFound = errors.New("quota cap revision not found")

// ErrQuotaCapNotCandidate is returned when activating/retiring a cap revision
// that is not in the expected lifecycle state.
var ErrQuotaCapNotCandidate = errors.New("quota cap revision is not a candidate")

// ErrQuotaCapLowerThanActivePolicy is returned when activating a cap revision
// whose limits are lower than at least one currently active quota-policy
// version. The administrator must deprecate/raise policies deliberately first.
var ErrQuotaCapLowerThanActivePolicy = errors.New("quota cap is lower than an active policy version")

// ErrInvalidPolicyVersionProvenance is returned when a policy version lacks the
// active-cap binding required for managed assignment (e.g. a pre-037 version
// with no cap_revision_id, or a version bound to a non-active cap).
var ErrInvalidPolicyVersionProvenance = errors.New("policy version missing active-cap provenance")

// quotaCapAdvisoryLockKey serializes every cap activation/replacement and every
// cap-aware policy-version append so the active-cap pointer and the
// next-version/next-revision sequences are computed one-at-a-time. It mirrors
// the role-boundary advisory lock design: a single fixed key; held for the
// duration of the transaction; released on commit/rollback.
const quotaCapAdvisoryLockKey int64 = 0x51434150544B5951 // "QCAPTKY"

// quotaAdmitAdvisoryLockKey serializes every resource-increasing quota admission
// for a SINGLE user (the key is derived from the user id) so that two concurrent
// operations that each evaluate quota and then persist a VM/disk cannot
// double-spend the same capacity. It is taken AFTER the cap lock (when needed)
// and BEFORE any quota read/persist, mirroring the established cap-lock ordering
// so the two locks never deadlock. On SQLite (unit tests) it is a no-op.
const quotaAdmitAdvisoryLockKey int64 = 0x5144414D49544B59 // "QADMITKY"

// AcquireQuotaAdmitLock takes the per-user advisory lock for resource-increasing
// quota admission. The key is hashed from the user id so distinct users admit in
// parallel while the same user serializes. The advisory xact lock is
// PostgreSQL-only; other dialects (SQLite) return nil and run inside whatever
// transaction the caller already opened (still serializable at the unit level).
// It is exported so the quota SERVICE can take the lock inside its own
// transaction wrapper, preserving the established cap-lock ordering.
func AcquireQuotaAdmitLock(tx *gorm.DB, userID string) error {
	if !dialectIsPostgres(tx) {
		return nil
	}
	key := quotaAdmitAdvisoryLockKey ^ int64(fnv32a(userID))
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error
}

// fnv32a is a small, dependency-free hash used to fold a user id into a stable
// 32-bit advisory lock key component. It is NOT cryptographic; it only needs to
// spread user ids across distinct lock keys.
func fnv32a(s string) uint32 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

// dialectIsPostgres reports whether the dialector targets PostgreSQL, where the
// transaction advisory lock is available.
func dialectIsPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// acquireQuotaCapLock takes the transaction-scoped advisory lock on PostgreSQL
// (a no-op elsewhere; SQLite unit tests still run inside a transaction).
func acquireQuotaCapLock(tx *gorm.DB) error {
	if !dialectIsPostgres(tx) {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", quotaCapAdvisoryLockKey).Error
}

// QuotaPolicyRepository provides data access for named, versioned account quota
// policies. Policy versions are append-only and policy/version entities are
// never physically deleted through this API (deprecation is the lifecycle exit).
type QuotaPolicyRepository struct {
	db    *gorm.DB
	qRepo *QuotaRepository
}

// NewQuotaPolicyRepository creates a new QuotaPolicyRepository.
func NewQuotaPolicyRepository(db *gorm.DB) *QuotaPolicyRepository {
	return &QuotaPolicyRepository{db: db, qRepo: NewQuotaRepository(db)}
}

// CreatePolicy inserts a new named policy. The first default policy must be the
// only active default (enforced by the partial unique index; the error is
// translated to ErrMultipleDefaultQuotaPolicy).
func (r *QuotaPolicyRepository) CreatePolicy(ctx context.Context, p *models.QuotaPolicy) error {
	err := r.db.WithContext(ctx).Create(p).Error
	if err != nil {
		return mapQuotaPolicyError(err)
	}
	return nil
}

// GetPolicy returns a policy with its versions loaded.
func (r *QuotaPolicyRepository) GetPolicy(ctx context.Context, id string) (*models.QuotaPolicy, error) {
	var p models.QuotaPolicy
	if err := r.db.WithContext(ctx).Preload("Versions").First(&p, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuotaPolicyNotFound
		}
		return nil, err
	}
	return &p, nil
}

// GetPolicyByName returns a policy by its unique name.
func (r *QuotaPolicyRepository) GetPolicyByName(ctx context.Context, name string) (*models.QuotaPolicy, error) {
	var p models.QuotaPolicy
	if err := r.db.WithContext(ctx).Preload("Versions").First(&p, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuotaPolicyNotFound
		}
		return nil, err
	}
	return &p, nil
}

// ListPolicies returns all policies ordered by name, optionally filtering to a
// single lifecycle state. Versions are eagerly loaded.
func (r *QuotaPolicyRepository) ListPolicies(ctx context.Context, lifecycle models.QuotaPolicyLifecycle) ([]models.QuotaPolicy, error) {
	var policies []models.QuotaPolicy
	q := r.db.WithContext(ctx).Preload("Versions").Order("name ASC")
	if lifecycle != "" {
		q = q.Where("lifecycle = ?", lifecycle)
	}
	if err := q.Find(&policies).Error; err != nil {
		return nil, err
	}
	return policies, nil
}

// SetPolicyLifecycle moves a policy between active/deprecated. Physical deletion
// is intentionally unsupported.
func (r *QuotaPolicyRepository) SetPolicyLifecycle(ctx context.Context, id string, lifecycle models.QuotaPolicyLifecycle) error {
	res := r.db.WithContext(ctx).Model(&models.QuotaPolicy{}).
		Where("id = ?", id).
		Update("lifecycle", lifecycle)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrQuotaPolicyNotFound
	}
	return nil
}

// SetDefaultPolicy marks the given policy as the single active default and
// clears is_default on every other active policy. It refuses to operate on a
// missing or deprecated policy (a deprecated policy can never be the default)
// and on a policy that has not yet published any immutable version, surfacing a
// clean domain error in each case rather than relying on a downstream DB failure.
// No implicit "first active" default is ever created.
func (r *QuotaPolicyRepository) SetDefaultPolicy(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p models.QuotaPolicy
		if err := tx.First(&p, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQuotaPolicyNotFound
			}
			return err
		}
		if p.Lifecycle != models.QuotaPolicyActive {
			// Covers both deprecated and any non-active lifecycle: a non-active
			// policy must never become the default.
			return ErrMultipleDefaultQuotaPolicy
		}

		// Require at least one published immutable version before a policy can be
		// the default; otherwise enrollment would have nothing to copy.
		var versionCount int64
		if err := tx.Model(&models.QuotaPolicyVersion{}).
			Where("policy_id = ?", id).
			Count(&versionCount).Error; err != nil {
			return err
		}
		if versionCount == 0 {
			return ErrQuotaPolicyHasNoVersion
		}

		// Atomically clear is_default on all other active policies, then set it on
		// this one. The partial unique index (is_default) WHERE active is the DB
		// backstop; the unique-violation branch is retained for the lost-race case.
		if err := tx.Model(&models.QuotaPolicy{}).
			Where("lifecycle = ? AND id <> ?", models.QuotaPolicyActive, id).
			Update("is_default", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.QuotaPolicy{}).
			Where("id = ?", id).
			Update("is_default", true).Error; err != nil {
			// A unique-violation here means we lost the race to be the single
			// default; surface it as a domain error.
			if isQuotaUniqueViolation(err) {
				return ErrMultipleDefaultQuotaPolicy
			}
			return err
		}
		return nil
	})
}

// ClearDefaultPolicy unsets is_default on every policy. Use when an admin wants
// no default at all (enrollment then requires an explicit policy choice).
func (r *QuotaPolicyRepository) ClearDefaultPolicy(ctx context.Context) error {
	return r.db.WithContext(ctx).Model(&models.QuotaPolicy{}).
		Where("is_default = ?", true).
		Update("is_default", false).Error
}

// GetDefaultPolicy returns the single active default policy, or
// ErrQuotaPolicyNotFound when none is set.
func (r *QuotaPolicyRepository) GetDefaultPolicy(ctx context.Context) (*models.QuotaPolicy, error) {
	var p models.QuotaPolicy
	if err := r.db.WithContext(ctx).Preload("Versions").
		First(&p, "is_default = ? AND lifecycle = ?", true, models.QuotaPolicyActive).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuotaPolicyNotFound
		}
		return nil, err
	}
	return &p, nil
}

// AppendVersion appends a new immutable version to a policy. It is the ONLY way
// to add limits; there is no update/delete method for versions. The parent
// policy row is locked with a blocking row-level FOR UPDATE lock inside an
// explicit transaction before the next version number is computed, so concurrent
// appenders wait on (rather than skip) the policy row and serialize their
// next-version computation. The (policy_id, version) unique constraint remains a
// secondary backstop. A deprecated policy cannot receive new versions.
//
// Cap awareness (Gate 1): a version can only be published while a platform
// quota-cap is active. The active cap is loaded (and the cap singleton row is
// row-locked) inside the transaction, and the version is bound to it via
// cap_revision_id. The DB trigger provides the authoritative cross-table
// enforcement under concurrent cap activation; this app-level check fails cleanly
// with ErrNoActiveQuotaCap when no cap is active so callers get a domain error
// rather than a raw trigger exception.
func (r *QuotaPolicyRepository) AppendVersion(ctx context.Context, policyID string, v *models.QuotaPolicyVersion) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize all cap-aware appends and cap activations on one advisory key.
		if err := acquireQuotaCapLock(tx); err != nil {
			return err
		}

		// Lock the parent policy row with a BLOCKING FOR UPDATE so the
		// next-version computation is serialized per policy. On PostgreSQL this
		// makes a concurrent appender wait for the lock to be released before it
		// can read the row and compute MAX(version)+1 (no SKIP LOCKED, which
		// would wrongly surface ErrQuotaPolicyNotFound for a row merely held by
		// another transaction). Under SQLite (used by tests) FOR UPDATE is a
		// no-op but the surrounding single-writer transaction still provides
		// isolation; actual multi-connection blocking needs live-Postgres proof.
		var p models.QuotaPolicy
		if err := tx.Clauses(clause.Locking{
			Strength: clause.LockingStrengthUpdate,
		}).First(&p, "id = ?", policyID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQuotaPolicyNotFound
			}
			return err
		}

		if p.Lifecycle != models.QuotaPolicyActive {
			// Deprecated (or otherwise non-active) policies cannot grow new
			// versions; publish a fresh policy instead.
			return ErrQuotaPolicyNotActive
		}

		// Require a live active cap; fail closed otherwise.
		activeCap, err := getActiveCapForUpdate(tx)
		if err != nil {
			return err
		}

		var maxVer int
		if err := tx.Model(&models.QuotaPolicyVersion{}).
			Where("policy_id = ?", policyID).
			Select("COALESCE(MAX(version), 0)").
			Scan(&maxVer).Error; err != nil {
			return err
		}

		v.PolicyID = policyID
		v.Version = maxVer + 1
		v.CapRevisionID = &activeCap.ID
		if err := tx.Create(v).Error; err != nil {
			return mapQuotaPolicyError(err)
		}
		return nil
	})
}

// getActiveCapForUpdate loads the single active cap revision, row-locking the
// singleton state row and the revision so concurrent cap activation and
// cap-aware appends observe a stable active cap. Returns ErrNoActiveQuotaCap when
// no cap is active. Must be called inside a transaction (with the advisory lock
// already held by callers that need serialization).
func getActiveCapForUpdate(tx *gorm.DB) (*models.PlatformQuotaCapRevision, error) {
	var state models.PlatformQuotaCapState
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&state, "singleton_key = ?", "A").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoActiveQuotaCap
		}
		return nil, err
	}
	if state.ActiveRevisionID == nil {
		return nil, ErrNoActiveQuotaCap
	}
	var cap models.PlatformQuotaCapRevision
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&cap, "id = ? AND state = ?", *state.ActiveRevisionID, models.PlatformCapActive).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoActiveQuotaCap
		}
		return nil, err
	}
	return &cap, nil
}

// CreateCapRevision stages a new candidate cap revision. The immutable revision
// number is assigned under the advisory lock to guarantee a monotonic, gap-free
// sequence. The candidate must have strictly positive, finite limits (enforced
// by the model CHECK and DB constraint).
func (r *QuotaPolicyRepository) CreateCapRevision(ctx context.Context, c *models.PlatformQuotaCapRevision, createdBy string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireQuotaCapLock(tx); err != nil {
			return err
		}
		var maxRev int64
		if err := tx.Model(&models.PlatformQuotaCapRevision{}).
			Select("COALESCE(MAX(revision), 0)").
			Scan(&maxRev).Error; err != nil {
			return err
		}
		c.Revision = maxRev + 1
		if createdBy != "" {
			c.CreatedBy = &createdBy
		}
		c.State = models.PlatformCapCandidate
		if err := tx.Create(c).Error; err != nil {
			return mapQuotaCapError(err)
		}
		return nil
	})
}

// ActivateCapRevision transitions a candidate cap to active, replacing any
// previously active cap (which is retired). It is serialized by the advisory
// lock and refuses to activate a cap LOWER than any currently active
// quota-policy version: the administrator must deprecate/raise policies first.
func (r *QuotaPolicyRepository) ActivateCapRevision(ctx context.Context, id string, actor string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireQuotaCapLock(tx); err != nil {
			return err
		}

		// Row-lock the candidate to be activated.
		var candidate models.PlatformQuotaCapRevision
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&candidate, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQuotaCapNotFound
			}
			return err
		}
		if candidate.State != models.PlatformCapCandidate {
			return ErrQuotaCapNotCandidate
		}

		// Do not allow a cap lower than any currently active policy version.
		if below, err := capBelowActivePolicyVersions(tx, &candidate); err != nil {
			return err
		} else if below {
			return ErrQuotaCapLowerThanActivePolicy
		}

		now := time.Now()
		// Retire the previously active cap, if any (row-lock the state row first).
		var state models.PlatformQuotaCapState
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&state, "singleton_key = ?", "A").Error; err != nil {
			return err
		}
		if state.ActiveRevisionID != nil && *state.ActiveRevisionID != candidate.ID {
			res := tx.Model(&models.PlatformQuotaCapRevision{}).
				Where("id = ? AND state = ?", *state.ActiveRevisionID, models.PlatformCapActive).
				Updates(map[string]interface{}{
					"state":      models.PlatformCapRetired,
					"retired_at": &now,
				})
			if res.Error != nil {
				return res.Error
			}
		}

		// Activate the candidate and point the singleton at it. The lifecycle
		// metadata written here (state=active, activated_at=now) is the exact
		// contract enforced by migration 037b's trg_platform_cap_revision_immutable
		// (active requires activated_at and no retired_at; activated_at is
		// immutable once set).
		if err := tx.Model(&models.PlatformQuotaCapRevision{}).
			Where("id = ?", candidate.ID).
			Updates(map[string]interface{}{
				"state":        models.PlatformCapActive,
				"activated_at": &now,
			}).Error; err != nil {
			return err
		}
		updBy := actor
		if err := tx.Model(&models.PlatformQuotaCapState{}).
			Where("singleton_key = ?", "A").
			Updates(map[string]interface{}{
				"active_revision_id": candidate.ID,
				"state":              models.PlatformCapStateActive,
				"updated_by":         &updBy,
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}
		return nil
	})
}

// RetireCapRevision retires a non-active cap revision (e.g. a stale candidate)
// or withdraws the active cap. Retiring the active cap intentionally leaves the
// system with no active cap; managed publishing/assignment then fails closed via
// ErrNoActiveQuotaCap. It refuses to retire a revision that is not a candidate
// unless it is the currently active one being withdrawn.
func (r *QuotaPolicyRepository) RetireCapRevision(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := acquireQuotaCapLock(tx); err != nil {
			return err
		}
		var c models.PlatformQuotaCapRevision
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&c, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrQuotaCapNotFound
			}
			return err
		}
		if c.State == models.PlatformCapRetired {
			return nil
		}
		now := time.Now()
		// Retire via the lifecycle contract enforced by 037b:
		//   * a stale candidate (activated_at IS NULL) retires with retired_at set,
		//     representing withdrawal-before-activation;
		//   * the currently active cap retires with activated_at unchanged and
		//     retired_at set, and the singleton pointer is then cleared so the
		//     system is left without an active cap.
		// note/limits remain immutable. Both paths are legal transitions under
		// trg_platform_cap_revision_immutable.
		updates := map[string]interface{}{"state": models.PlatformCapRetired, "retired_at": &now}
		if err := tx.Model(&models.PlatformQuotaCapRevision{}).
			Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		// If this was the active cap, clear the singleton pointer.
		if err := tx.Model(&models.PlatformQuotaCapState{}).
			Where("singleton_key = ? AND active_revision_id = ?", "A", id).
			Updates(map[string]interface{}{
				"active_revision_id": nil,
				"state":              models.PlatformCapStateInactive,
			}).Error; err != nil {
			return err
		}
		return nil
	})
}

// GetActiveCapRevision returns the single active cap revision, or
// ErrNoActiveQuotaCap when none is active.
func (r *QuotaPolicyRepository) GetActiveCapRevision(ctx context.Context) (*models.PlatformQuotaCapRevision, error) {
	var state models.PlatformQuotaCapState
	if err := r.db.WithContext(ctx).First(&state, "singleton_key = ?", "A").Error; err != nil {
		return nil, err
	}
	if state.ActiveRevisionID == nil {
		return nil, ErrNoActiveQuotaCap
	}
	var c models.PlatformQuotaCapRevision
	if err := r.db.WithContext(ctx).
		First(&c, "id = ? AND state = ?", *state.ActiveRevisionID, models.PlatformCapActive).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNoActiveQuotaCap
		}
		return nil, err
	}
	return &c, nil
}

// ListCapRevisions returns all cap revisions ordered by revision descending.
func (r *QuotaPolicyRepository) ListCapRevisions(ctx context.Context) ([]models.PlatformQuotaCapRevision, error) {
	var caps []models.PlatformQuotaCapRevision
	if err := r.db.WithContext(ctx).Order("revision DESC").Find(&caps).Error; err != nil {
		return nil, err
	}
	return caps, nil
}

// capBelowActivePolicyVersions reports whether the candidate cap's limits are
// lower than the maximum limit of any currently active quota-policy version.
// "Active policy version" means a version belonging to a policy whose lifecycle
// is 'active'. This prevents activating a ceiling below what is already assigned.
func capBelowActivePolicyVersions(tx *gorm.DB, c *models.PlatformQuotaCapRevision) (bool, error) {
	type agg struct {
		MaxVMs    int
		MaxVCPU   int
		MaxRAMMB  int
		MaxDiskGB int
	}
	var a agg
	// Raw SQL avoids GORM join/alias ambiguity on SQLite while remaining valid
	// on PostgreSQL. MAX over an empty set yields NULL, so COALESCE guards it.
	err := tx.Raw(`
		SELECT COALESCE(MAX(v.max_vms), 0)  AS max_vms,
		       COALESCE(MAX(v.max_vcpu), 0) AS max_vcpu,
		       COALESCE(MAX(v.max_ram_mb), 0) AS max_ram_mb,
		       COALESCE(MAX(v.max_disk_gb), 0) AS max_disk_gb
		FROM quota_policy_versions v
		JOIN quota_policies p ON p.id = v.policy_id
		WHERE p.lifecycle = ?`, models.QuotaPolicyActive).Scan(&a).Error
	if err != nil {
		return false, err
	}
	if c.MaxVMs < a.MaxVMs || c.MaxVCPU < a.MaxVCPU || c.MaxRAMMB < a.MaxRAMMB || c.MaxDiskGB < a.MaxDiskGB {
		return true, nil
	}
	return false, nil
}

// mapQuotaCapError translates driver errors into domain errors so callers do
// not depend on Postgres error codes.
func mapQuotaCapError(err error) error {
	if err == nil {
		return nil
	}
	if isQuotaUniqueViolation(err) {
		msg := err.Error()
		if qpContains(msg, "platform_quota_cap_revisions_revision") || qpContains(msg, "platform_quota_cap_revisions.revision") {
			return ErrDuplicatePolicyVersion
		}
	}
	return err
}

// GetVersion returns a specific immutable version.
func (r *QuotaPolicyRepository) GetVersion(ctx context.Context, policyID string, version int) (*models.QuotaPolicyVersion, error) {
	var v models.QuotaPolicyVersion
	if err := r.db.WithContext(ctx).First(&v, "policy_id = ? AND version = ?", policyID, version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuotaPolicyNotFound
		}
		return nil, err
	}
	return &v, nil
}

// ListVersions returns every immutable version for a policy ordered by version.
func (r *QuotaPolicyRepository) ListVersions(ctx context.Context, policyID string) ([]models.QuotaPolicyVersion, error) {
	var versions []models.QuotaPolicyVersion
	if err := r.db.WithContext(ctx).
		Where("policy_id = ?", policyID).
		Order("version ASC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// AssignToUserQuota writes the snapshot of an immutable version into a user's
// effective quota (user_quotas) and records the provenance. This does NOT keep a
// live dependency: it copies the limits at assignment time. It also flips the
// row into managed mode. The caller remains responsible for transaction/
// enrollment orchestration in Phase 1B.
//
// AssignToUserQuota is the non-Tx convenience wrapper around the cap-aware
// managed assignment in QuotaRepository. It opens a transaction (never a silent
// base-DB fallback) so the same Gate-1 contract applies: the user must be
// managed, an active platform cap must exist, and the version must carry valid
// active-cap provenance. The policy repository delegates to QuotaRepository so
// there is a single managed-assignment implementation.
func (r *QuotaPolicyRepository) AssignToUserQuota(ctx context.Context, userID string, v *models.QuotaPolicyVersion, assignedBy string) error {
	return r.qRepo.AssignToUserQuota(ctx, userID, v, assignedBy)
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// mapQuotaPolicyError translates driver errors into domain errors so callers do
// not depend on Postgres error codes.
func mapQuotaPolicyError(err error) error {
	if err == nil {
		return nil
	}
	if isQuotaUniqueViolation(err) {
		// Disambiguate name vs (policy_id, version) collisions by message text.
		msg := err.Error()
		if qpContains(msg, "quota_policies_name") || qpContains(msg, "quota_policies.name") {
			return ErrDuplicateQuotaPolicyName
		}
		if qpContains(msg, "quota_policy_versions_policy_version_uniq") {
			return ErrDuplicatePolicyVersion
		}
		if qpContains(msg, "quota_policies_single_active_default") {
			return ErrMultipleDefaultQuotaPolicy
		}
	}
	return err
}

// isQuotaUniqueViolation reports whether err is a uniqueness violation. The check
// is string-based so it also works under the SQLite test driver, which surfaces
// UNIQUE constraint failures as errors containing "UNIQUE" and the table/column
// name rather than a Postgres SQLSTATE 23505.
func isQuotaUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return qpContains(msg, "23505") || (qpContains(msg, "UNIQUE") && qpContains(msg, "quota"))
}

func qpContains(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
