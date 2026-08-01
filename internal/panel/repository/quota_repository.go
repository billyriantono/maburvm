package repository

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrUserNotManaged is returned by AssignToUserQuotaTx when the target user is
// not flagged managed (a managed quota snapshot must not be written for a legacy
// user).
var ErrUserNotManaged = errors.New("user is not managed")

// ErrUserNotFound is returned by Upsert (and surfaced through QuotaService) when
// the target user row does not exist. A direct quota write without a backing user
// is meaningless, so it is rejected rather than creating an orphan quota row. It
// is a distinct sentinel from ErrManagedQuotaDirectMutation so the handler can
// map an absent user to a generic 404 instead of the managed-state 409.
var ErrUserNotFound = errors.New("user not found")

// ErrManagedQuotaInvalid is returned when a managed user's quota row is missing
// finite positive limits or valid provenance and therefore cannot be trusted.
var ErrManagedQuotaInvalid = errors.New("managed user quota is missing or invalid")

// ErrManagedQuotaDirectMutation is returned by Upsert (and by QuotaService.SetQuota
// which delegates to it) when a caller attempts a legacy direct quota write for a
// user whose authoritative users.quota_mode is 'managed'. Managed accounts must be
// provisioned exclusively through AssignToUserQuotaTx, which carries the full
// cap-bound policy provenance. A direct (legacy) upsert would silently clobber or
// bypass the managed snapshot, so it is rejected here instead.
var ErrManagedQuotaDirectMutation = errors.New("direct quota mutation is not permitted for a managed user")

// ErrQuotaNegative is returned when a direct (legacy) quota write carries a
// negative limit. A negative limit is INVALID and must NEVER mean unlimited.
// This is a defensive backstop: the service already rejects negative input before
// calling Upsert, and migration 040 adds a hard nonnegative CHECK at the
// PostgreSQL level. We reject here too so an in-process caller cannot bypass the
// service guard.
var ErrQuotaNegative = errors.New("quota limits must be non-negative")

// ErrDiskInventoryOverflow is returned when summing a user's disk accounting
// (boot disks + extra disks + pending reservations) would overflow int64. Disk
// accounting fails CLOSED on overflow rather than silently wrapping.
var ErrDiskInventoryOverflow = errors.New("disk inventory overflow")

// ErrInvalidDiskInventory is returned when a disk accounting source carries an
// invalid (negative) size, which must never be counted against quota.
var ErrInvalidDiskInventory = errors.New("invalid disk inventory")

// ErrReservationRepoRequired is returned when a disk-accounting call is invoked
// with a nil reservation repository. Pending reservations are ALWAYS part of the
// authoritative disk total; callers must never be able to omit them by passing
// nil (that would under-count admission and status).
var ErrReservationRepoRequired = errors.New("disk accounting requires a non-nil reservation repository")

// ErrResourceOverflow is returned when summing/ projecting any resource dimension
// (VMs, vCPU, RAM, disk) would overflow the int64 representation. Resource
// accounting fails CLOSED on overflow rather than silently wrapping.
var ErrResourceOverflow = errors.New("resource usage overflow")

// ErrQuotaTxRequired is returned by service admission/evaluation helpers that
// MUST run inside a caller-supplied transaction so the quota read, lock, and the
// caller's subsequent accounting write are atomic. They refuse a nil transaction.
var ErrQuotaTxRequired = errors.New("quota operation requires a caller-supplied transaction")

// requireTx returns ErrQuotaTxRequired when tx is nil so Tx-bound helpers never
// silently fall back to the base DB handle.
func requireTx(tx *gorm.DB) error {
	if tx == nil {
		return ErrQuotaTxRequired
	}
	return nil
}

// QuotaRepository provides data access for per-user resource quotas.
type QuotaRepository struct {
	db *gorm.DB
}

// NewQuotaRepository creates a new QuotaRepository.
func NewQuotaRepository(db *gorm.DB) *QuotaRepository {
	return &QuotaRepository{db: db}
}

// GetByUserID returns the quota row for a user, or gorm.ErrRecordNotFound.
func (r *QuotaRepository) GetByUserID(ctx context.Context, userID string) (*models.UserQuota, error) {
	return r.GetByUserIDOn(ctx, r.db, userID)
}

// GetByUserIDOn returns the quota row for a user using the supplied handle
// (a transaction for admission paths, or the base DB for the public read path).
// It never falls back to a different handle: resolution always uses the SAME
// authoritative handle that the mode was read from.
func (r *QuotaRepository) GetByUserIDOn(ctx context.Context, handle *gorm.DB, userID string) (*models.UserQuota, error) {
	var q models.UserQuota
	if err := handle.WithContext(ctx).First(&q, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// GetByUserIDTx returns the quota row for a user within an explicit transaction.
// Used by admission paths that must evaluate quota under the same transaction
// that holds the per-user advisory lock so concurrent increases serialize.
func (r *QuotaRepository) GetByUserIDTx(ctx context.Context, tx *gorm.DB, userID string) (*models.UserQuota, error) {
	return r.GetByUserIDOn(ctx, tx, userID)
}

// DiskUsageTx computes a user's disk consumption INSIDE the supplied transaction
// so admission reads see a consistent snapshot relative to the per-user advisory
// lock. It is a thin wrapper over TotalDiskAccountingTx (the single source of
// truth for disk accounting) so status and admission never diverge. It requires a
// non-nil tx and resRepo (no base-DB / nil-reservation bypass is permitted).
//
// All summation uses checked int64 arithmetic and fails closed on overflow or any
// invalid (negative) inventory.
func (r *QuotaRepository) DiskUsageTx(ctx context.Context, tx *gorm.DB, userID string, resRepo *DiskQuotaReservationRepository) (int, error) {
	if err := requireTx(tx); err != nil {
		return 0, err
	}
	if resRepo == nil {
		return 0, ErrReservationRepoRequired
	}
	total, err := r.TotalDiskAccountingTx(ctx, tx, userID, resRepo)
	if err != nil {
		return 0, err
	}
	if total > int64(math.MaxInt) {
		return 0, ErrDiskInventoryOverflow
	}
	return int(total), nil
}

// TotalDiskAccountingTx is the authoritative, transaction-scoped disk accounting
// source for a user. It returns the same total used by EVERY quota read (status,
// admission, and future VM/worker lanes) so no path can disagree:
//   - boot disks (vm.Resources.Disk) from all VMs owned by the user, INCLUDING
//     soft-deleted VMs. A soft-deleted VM remains quota-counted until its physical
//     hard deletion (Lane E); only a hard-deleted VM drops from accounting. We
//     therefore read vms with Unscoped so a soft-deleted (deleted_at set) VM is
//     still counted.
//   - vm_disks rows in BOTH the 'attached' AND 'deleting' lifecycle that are NOT
//     hard-deleted (deleted_at IS NULL). A deleting disk is still physically
//     allocated until the agent certifies destruction and the panel worker
//     hard-deletes the row.
//   - pending disk-quota reservations for the user (over-count for serialization).
//
// resRepo MUST be non-nil: pending reservations are always part of the total and a
// nil bypass would under-count admission/status. The call fails closed with
// ErrReservationRepoRequired when nil.
//
// It uses checked int64 arithmetic; overflow or a negative inventory value fails
// closed. All reads are scoped to the supplied tx (never the base DB).
func (r *QuotaRepository) TotalDiskAccountingTx(ctx context.Context, tx *gorm.DB, userID string, resRepo *DiskQuotaReservationRepository) (int64, error) {
	if err := requireTx(tx); err != nil {
		return 0, err
	}
	if resRepo == nil {
		return 0, ErrReservationRepoRequired
	}

	// Include soft-deleted VMs: a soft-deleted VM is still counted until its
	// physical hard deletion (Lane E). Unscoped reads rows regardless of the
	// soft-delete marker, and we intentionally do NOT filter on deleted_at, so a
	// soft-deleted VM (deleted_at set) remains in the boot-disk accounting. Only a
	// HARD-deleted VM (row removed from the table) drops out.
	var vms []models.VM
	if err := tx.WithContext(ctx).
		Unscoped().
		Where("user_id = ?", userID).
		Find(&vms).Error; err != nil {
		return 0, err
	}
	vmIDs := make([]string, 0, len(vms))
	for i := range vms {
		if vms[i].Resources.Disk < 0 {
			return 0, ErrInvalidDiskInventory
		}
		vmIDs = append(vmIDs, vms[i].ID)
	}

	// Non-hard-deleted vm_disks in BOTH attached and deleting lifecycle states
	// count. A deleting disk is still physically allocated until agent cert.
	// (We do not apply Unscoped here: a hard-deleted vm_disks row must not count.
	// The migration 040a lifecycle contract guarantees DiskQuotaReservation hard
	// releases and vm_disks hard-deletion are the only removes.)
	var disks []models.VMDisk
	if len(vmIDs) > 0 {
		if err := tx.WithContext(ctx).
			Where("vm_id IN ? AND deleted_at IS NULL", vmIDs).
			Find(&disks).Error; err != nil {
			return 0, err
		}
	}

	total := int64(0)
	// Boot disks.
	for i := range vms {
		if vms[i].Resources.Disk > 0 {
			var ok bool
			total, ok = addChecked64(total, int64(vms[i].Resources.Disk))
			if !ok {
				return 0, ErrDiskInventoryOverflow
			}
		}
	}
	// Extra disks (attached + deleting), both counted.
	for i := range disks {
		if disks[i].SizeGB < 0 {
			return 0, ErrInvalidDiskInventory
		}
		if disks[i].SizeGB > 0 {
			var ok bool
			total, ok = addChecked64(total, int64(disks[i].SizeGB))
			if !ok {
				return 0, ErrDiskInventoryOverflow
			}
		}
	}
	// Pending reservations (over-count).
	pending, perr := resRepo.PendingDiskGBTx(ctx, tx, userID)
	if perr != nil {
		return 0, perr
	}
	if pending < 0 {
		return 0, ErrInvalidDiskInventory
	}
	var ok bool
	total, ok = addChecked64(total, pending)
	if !ok {
		return 0, ErrDiskInventoryOverflow
	}

	return total, nil
}

// ComputeUsageTx is the authoritative, transaction-scoped resource-usage source
// for a user. It is the single source used by status (GetStatus) and by the
// evaluation cores so status and admission never diverge. It counts VMs (including
// soft-deleted, which remain counted until physical hard deletion) and sums their
// CPU/RAM/boot-disk using checked int64 arithmetic, and adds the authoritative disk
// total (boot + non-hard-deleted attached/deleting extra disks + pending
// reservations) via TotalDiskAccountingTx. The returned totals are int (the wire
// type) but intermediate summation is int64 and overflow fails closed.
//
// resRepo MUST be non-nil (it is the same requirement as TotalDiskAccountingTx).
func (r *QuotaRepository) ComputeUsageTx(ctx context.Context, tx *gorm.DB, userID string, resRepo *DiskQuotaReservationRepository) (UsageTotals, error) {
	if err := requireTx(tx); err != nil {
		return UsageTotals{}, err
	}
	if resRepo == nil {
		return UsageTotals{}, ErrReservationRepoRequired
	}

	// Include soft-deleted VMs: counted until physical hard deletion (Lane E).
	// Unscoped reads regardless of the soft-delete marker; no deleted_at filter.
	var vms []models.VM
	if err := tx.WithContext(ctx).
		Unscoped().
		Where("user_id = ?", userID).
		Find(&vms).Error; err != nil {
		return UsageTotals{}, err
	}

	var out UsageTotals
	out.VMs = len(vms)
	for i := range vms {
		res := vms[i].Resources
		if res.CPU < 0 || res.RAM < 0 || res.Disk < 0 {
			return UsageTotals{}, ErrInvalidDiskInventory
		}
		v, ok := addChecked64(out.VCPU, int64(res.CPU))
		if !ok {
			return UsageTotals{}, ErrResourceOverflow
		}
		out.VCPU = v
		r, ok := addChecked64(out.RAMMB, int64(res.RAM))
		if !ok {
			return UsageTotals{}, ErrResourceOverflow
		}
		out.RAMMB = r
		// Do NOT accumulate DiskGB here: the authoritative disk total (boot from
		// these same VMs + attached/deleting extra disks + pending reservations)
		// is set below from TotalDiskAccountingTx so boot is counted exactly once.
	}

	// The authoritative disk total (boot + non-hard-deleted attached/deleting
	// extra disks + pending reservations) from TotalDiskAccountingTx. Boot disks
	// are counted once here.
	diskTotal, err := r.TotalDiskAccountingTx(ctx, tx, userID, resRepo)
	if err != nil {
		return UsageTotals{}, err
	}
	out.DiskGB = diskTotal

	return out, nil
}

// UsageTotals is the int64 resource-usage projection used internally by the
// quota core. It mirrors QuotaUsage but uses int64 intermediate sums.
type UsageTotals struct {
	VMs    int
	VCPU   int64
	RAMMB  int64
	DiskGB int64
}

// addChecked64 adds two int64 values and reports whether the sum overflowed.
func addChecked64(a, b int64) (int64, bool) {
	sum := a + b
	// Overflow occurs when the signs of a and b are equal but differ from sum.
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum > 0) {
		return 0, false
	}
	return sum, true
}

// GetUserQuotaMode returns the user-level legacy/managed marker for a user, or
// gorm.ErrRecordNotFound when the user does not exist.
func (r *QuotaRepository) GetUserQuotaMode(ctx context.Context, userID string) (models.QuotaMode, error) {
	return r.GetUserQuotaModeOn(ctx, r.db, userID)
}

// GetUserQuotaModeOn returns the authoritative user-level quota_mode using the
// supplied handle. For transaction admission paths it is the caller's tx; for the
// public read path it is the base DB. The marker is ALWAYS read from the SAME
// authoritative handle used to read the quota row, so the two can never disagree.
func (r *QuotaRepository) GetUserQuotaModeOn(ctx context.Context, handle *gorm.DB, userID string) (models.QuotaMode, error) {
	var u models.User
	if err := handle.WithContext(ctx).Select("quota_mode").First(&u, "id = ?", userID).Error; err != nil {
		return "", err
	}
	return u.QuotaMode, nil
}

// GetUserQuotaModeTx returns the authoritative user-level quota_mode INSIDE the
// supplied transaction, taking a FOR UPDATE row lock on the user row. Admission/
// status resolution must use this so the mode agrees with the row read under the
// same transaction that holds the per-user admission lock, preventing a
// mode/row-mode split. A nil tx is rejected (no base-DB fallback). On SQLite the
// row lock is a no-op but the tx-bound read still prevents a base-DB bypass.
func (r *QuotaRepository) GetUserQuotaModeTx(ctx context.Context, tx *gorm.DB, userID string) (models.QuotaMode, error) {
	if err := requireTx(tx); err != nil {
		return "", err
	}
	var u models.User
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("quota_mode").First(&u, "id = ?", userID).Error; err != nil {
		return "", err
	}
	return u.QuotaMode, nil
}

// Upsert creates or updates the quota row for a user. It deliberately does NOT
// touch the quota_mode column, so a legacy user's mode is preserved when an admin
// sets their direct (legacy) quota. Managed snapshots are written exclusively via
// AssignToUserQuotaTx, never through this path.
//
// Upsert is the legacy (direct) writer, NOT a managed writer. To prevent a legacy
// admin path from silently clobbering a managed snapshot, it transactionally
// locks and re-checks users.quota_mode for the target user under a consistent
// FOR UPDATE row lock (a no-op on SQLite). The authoritative user row is locked
// so a concurrent managed conversion / assignment cannot race this read into a
// partial/legacy state. If the authoritative user mode is 'managed', the direct
// write is rejected with ErrManagedQuotaDirectMutation. A missing user row is
// likewise rejected (a direct quota write without a user is meaningless; it must
// not create an orphan quota row); the absent user is reported with ErrUserNotFound
// so callers can map it to a distinct 404 rather than the managed-state 409.
//
// For allowed legacy direct writes, the input snapshot is defensively stamped
// quota_mode='legacy' and every managed provenance column (policy_id,
// policy_version, policy_name, policy_assigned_at, policy_assigned_by,
// cap_revision_id) is cleared in BOTH the insert input and the ON CONFLICT update
// assignment. This guarantees a stale managed snapshot cannot be "downgraded" by
// a legacy write that leaves dangling managed metadata behind. Legacy
// zero/unlimited behavior is preserved.
//
// NOTE on policy versions: a higher (or merely newer) active cap does NOT
// authorize reusing an older cap-bound policy version. The cap binding is checked
// in AssignToUserQuotaTx; if the active cap advanced, a NEW policy version bound
// to the new cap_revision_id must be published before assignment. The SQL lane
// (migration 039) enforces the exact policy-version <-> cap-revision equality at
// the database level; this Go path only guards the managed/direct boundary.
func (r *QuotaRepository) Upsert(ctx context.Context, q *models.UserQuota) error {
	// Defensive backstop: a negative limit is invalid and must never be written
	// (it must not mean unlimited). The service rejects this too, but reject here
	// so an in-process caller cannot bypass the service guard. Note: zero remains
	// the explicit unlimited sentinel for legacy accounts.
	if q.MaxVMs < 0 || q.MaxVCPU < 0 || q.MaxRAMMB < 0 || q.MaxDiskGB < 0 {
		return ErrQuotaNegative
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock and read the authoritative user row. The FOR UPDATE lock (no-op on
		// SQLite) prevents a concurrent managed conversion/assignment from racing
		// this check. Use a dedicated GORM clause here rather than the cap
		// advisory lock: Upsert is a legacy writer and must not serialize on the
		// cap key; the row lock is the correct, minimal guard for the
		// managed/direct boundary.
		var u models.User
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("quota_mode").First(&u, "id = ?", q.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// No user row: a direct quota write without a user is meaningless;
				// report the absent user with a distinct sentinel so callers map
				// it to a 404 rather than creating an orphan quota row.
				return ErrUserNotFound
			}
			return err
		}
		if u.QuotaMode == models.QuotaModeManaged {
			return ErrManagedQuotaDirectMutation
		}

		// Defensively normalize the legacy direct write: stamp legacy mode and
		// clear all managed provenance so a previously-managed row cannot retain
		// stale managed metadata after a legacy admin override.
		q.QuotaMode = models.QuotaModeLegacy
		q.PolicyID = nil
		q.PolicyVersion = nil
		q.PolicyName = nil
		q.PolicyAssignedAt = nil
		q.PolicyAssignedBy = nil
		q.CapRevisionID = nil

		managedProvenanceCols := []string{
			"policy_id", "policy_version", "policy_name",
			"policy_assigned_at", "policy_assigned_by", "cap_revision_id",
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns(append([]string{
				"max_vms", "max_vcpu", "max_ram_mb", "max_disk_gb",
				"quota_mode", "updated_at",
			}, managedProvenanceCols...)),
		}).Create(q).Error
	})
}

// AssignToUserQuotaTx writes a full, finite, provenance-complete managed snapshot
// into user_quotas from an immutable policy version. It REQUIRES a non-nil
// transaction (there is no silent base-DB fallback) so it can participate in an
// outer enrollment transaction that rolls back cleanly.
//
// Contract (Gate 1):
//   - user must be flagged managed (users.quota_mode = 'managed'), else fail closed.
//   - an active platform cap must exist (fail closed otherwise).
//   - the policy version must exist, be bound to that active cap (cap_revision_id
//     matches), and carry finite positive limits; pre-037 versions without a cap
//     binding are rejected.
//   - the resulting snapshot copies the limits, the policy name (loaded here), and
//     full provenance (policy id/version/name, assigned_at/by, cap revision id).
//
// The policy version row is row-locked (FOR UPDATE) so a concurrent activation of
// a lower cap cannot race the read.
func (r *QuotaRepository) AssignToUserQuotaTx(ctx context.Context, tx *gorm.DB, userID string, policyVersion *models.QuotaPolicyVersion, assignedBy string) error {
	if tx == nil {
		return errors.New("AssignToUserQuotaTx requires a non-nil transaction")
	}

	// 1) The user must be managed.
	var u models.User
	if err := tx.WithContext(ctx).Select("quota_mode").First(&u, "id = ?", userID).Error; err != nil {
		return err
	}
	if u.QuotaMode != models.QuotaModeManaged {
		return ErrUserNotManaged
	}

	// 2) An active cap must exist and the chosen version must be bound to it.
	//    Participate in the exact same transaction-scoped advisory serialization
	//    used by AppendVersion / ActivateCapRevision so the active-cap pointer
	//    and the version->cap binding are observed under a stable, serialized
	//    snapshot. The advisory lock must be taken BEFORE reading the active cap
	//    row (identical lock order to those callers) to prevent a conversion /
	//    cap replacement from racing the read.
	if err := acquireQuotaCapLock(tx); err != nil {
		return err
	}
	activeCap, err := getActiveCapForUpdate(tx)
	if err != nil {
		return err
	}

	// 3) Load the version (with row lock) and verify provenance + finiteness.
	var v models.QuotaPolicyVersion
	if policyVersion != nil && policyVersion.ID != "" {
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&v, "id = ?", policyVersion.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidPolicyVersionProvenance
			}
			return err
		}
	} else if policyVersion != nil {
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&v, "policy_id = ? AND version = ?", policyVersion.PolicyID, policyVersion.Version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidPolicyVersionProvenance
			}
			return err
		}
	} else {
		return ErrInvalidPolicyVersionProvenance
	}

	if v.CapRevisionID == nil || *v.CapRevisionID != activeCap.ID {
		return ErrInvalidPolicyVersionProvenance
	}
	if v.MaxVMs <= 0 || v.MaxVCPU <= 0 || v.MaxRAMMB <= 0 || v.MaxDiskGB <= 0 {
		return ErrManagedQuotaInvalid
	}

	// 4) Load the policy name for the snapshot.
	var policy models.QuotaPolicy
	if err := tx.WithContext(ctx).First(&policy, "id = ?", v.PolicyID).Error; err != nil {
		return err
	}

	now := time.Now()
	snapshot := models.UserQuota{
		UserID:           userID,
		MaxVMs:           v.MaxVMs,
		MaxVCPU:          v.MaxVCPU,
		MaxRAMMB:         v.MaxRAMMB,
		MaxDiskGB:        v.MaxDiskGB,
		QuotaMode:        models.QuotaModeManaged,
		PolicyID:         &v.PolicyID,
		PolicyVersion:    &v.Version,
		PolicyName:       &policy.Name,
		PolicyAssignedAt: &now,
		PolicyAssignedBy: strPtrOrNil(assignedBy),
		CapRevisionID:    &activeCap.ID,
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"max_vms", "max_vcpu", "max_ram_mb", "max_disk_gb",
			"quota_mode", "policy_id", "policy_version", "policy_name",
			"policy_assigned_at", "policy_assigned_by", "cap_revision_id", "updated_at",
		}),
	}).Create(&snapshot).Error
}

// AssignToUserQuota is the non-Tx convenience wrapper around AssignToUserQuotaTx.
// It opens a transaction (never a silent base-DB fallback) so the same contract
// applies; the Tx method remains the primitive for callers that need to compose
// assignment inside a larger outer transaction (e.g. invite acceptance).
func (r *QuotaRepository) AssignToUserQuota(ctx context.Context, userID string, v *models.QuotaPolicyVersion, assignedBy string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.AssignToUserQuotaTx(ctx, tx, userID, v, assignedBy)
	})
}
