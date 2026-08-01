package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDiskReservationNotFound is returned when a disk-quota reservation does not
// exist or is not in a pending state for the requested VM/owner.
var ErrDiskReservationNotFound = errors.New("disk quota reservation not found")

// ErrDiskReservationConflict is returned when a reservation lifecycle transition
// is attempted from an unexpected state (e.g. consuming an already-consumed one,
// or creating a second pending reservation for a VM that already has one).
var ErrDiskReservationConflict = errors.New("disk quota reservation state conflict")

// ErrDiskReservationFinalizationRequired is returned by the DEPRECATED raw
// ConsumeDiskReservationTx compatibility surface. A safe attach finalization MUST
// go through the canonical LockAndFinalizeReservationTx primitive (which creates
// the VMDisk from the locked reservation inside the same transaction), not a raw
// consume that would permit a caller-controlled accounting swap. The current
// unmodified vm.go attach path is intentionally failed closed with this error so
// the pending reservation is RETAINED (never silently released into an unsafe
// state) until Lane D wires the canonical primitive.
var ErrDiskReservationFinalizationRequired = errors.New("use LockAndFinalizeReservationTx; raw consume is not a safe attach finalization")

// DiskQuotaReservationRepository provides durable lifecycle access for pending
// extra-disk admission reservations. Every writer accepts an explicit transaction
// and REQUIRES a non-nil tx (no base-DB fallback) so representation-changing
// operations always run inside the caller's admission transaction, after the
// per-user admission advisory lock has been taken.
//
// All state transitions use CONDITIONAL updates/deletes (a WHERE clause that
// includes the expected status) and REQUIRE RowsAffected == 1, failing closed with
// a typed error when the row is missing or no longer in the expected state. This
// guarantees exactly-once semantics for consume and release even under concurrent
// admission for the same VM.
//
// RELEASE IS HARD (physical DELETE), never a soft delete: the model carries no
// gorm.DeletedAt field. There is NO automatic expiry; a pending reservation
// intentionally over-counts rather than permit an agent-attached disk to bypass
// quota. Only a final DB-recording failure AFTER agent success retains the
// reservation fail-closed (handled by the application layer).
//
// Bulk pending-reservation removal (DeleteByVMIDTx) is exported for terminal VM
// teardown only (it is invoked by QuotaService.DeleteDiskReservationsByVMTx, used
// by VM teardown). Ordinary disk detach must use ReleaseTx (a single, specified
// reservation) or the canonical FinalizeTx, never the bulk helper.
type DiskQuotaReservationRepository struct {
	db *gorm.DB
}

// NewDiskQuotaReservationRepository creates a new DiskQuotaReservationRepository.
func NewDiskQuotaReservationRepository(db *gorm.DB) *DiskQuotaReservationRepository {
	return &DiskQuotaReservationRepository{db: db}
}

// WithDB returns a repository bound to the supplied database handle/transaction.
func (r *DiskQuotaReservationRepository) WithDB(db *gorm.DB) *DiskQuotaReservationRepository {
	return NewDiskQuotaReservationRepository(db)
}

// CreateTx inserts a PENDING reservation inside the caller's transaction. The
// caller must have already validated the disk-quota admission (check) and taken
// the per-user admission advisory lock, so this is a pure persistence step.
//
// Fails closed with ErrDiskReservationConflict when a pending reservation already
// exists for the same VM (the one-pending-per-VM invariant). The DB enforces this
// via a partial unique index; on SQLite (tests) the index is mirrored in the
// schema and on PostgreSQL it is migration 040/040a. Any other write error
// propagates.
func (r *DiskQuotaReservationRepository) CreateTx(ctx context.Context, tx *gorm.DB, res *models.DiskQuotaReservation) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	if res.Status == "" {
		res.Status = models.DiskQuotaReservationPending
	}
	if res.Status != models.DiskQuotaReservationPending {
		return ErrDiskReservationConflict
	}
	if res.SizeGB <= 0 {
		// A non-positive disk is invalid; the model/DB CHECK also rejects it, but
		// we fail closed before touching the wire.
		return ErrDiskReservationConflict
	}
	err := tx.WithContext(ctx).Create(res).Error
	if err != nil {
		// A unique violation on the one-pending-per-VM partial index is a conflict.
		if isQuotaUniqueViolation(err) {
			return ErrDiskReservationConflict
		}
		return err
	}
	return nil
}

// GetPendingTx reads a pending reservation by ID within the caller's transaction
// (no row lock). Returns ErrDiskReservationNotFound when the row is missing or not
// pending.
func (r *DiskQuotaReservationRepository) GetPendingTx(ctx context.Context, tx *gorm.DB, id string) (*models.DiskQuotaReservation, error) {
	if err := requireTx(tx); err != nil {
		return nil, err
	}
	var res models.DiskQuotaReservation
	if err := tx.WithContext(ctx).
		Where("id = ? AND status = ?", id, models.DiskQuotaReservationPending).
		First(&res).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDiskReservationNotFound
		}
		return nil, err
	}
	return &res, nil
}

// GetPendingForVMTx reads the (at most one) pending reservation for a VM within the
// caller's transaction (no row lock). Returns ErrDiskReservationNotFound when there
// is none.
func (r *DiskQuotaReservationRepository) GetPendingForVMTx(ctx context.Context, tx *gorm.DB, vmID string) (*models.DiskQuotaReservation, error) {
	if err := requireTx(tx); err != nil {
		return nil, err
	}
	var res models.DiskQuotaReservation
	if err := tx.WithContext(ctx).
		Where("vm_id = ? AND status = ?", vmID, models.DiskQuotaReservationPending).
		First(&res).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDiskReservationNotFound
		}
		return nil, err
	}
	return &res, nil
}

// LockPendingTx locks and reads a pending reservation by ID for UPDATE within the
// caller's transaction. The caller uses the locked row to derive authoritative
// vm_id / size_gb for finalization (never caller-controlled values). Returns
// ErrDiskReservationNotFound when missing or not pending; the row is FOR UPDATE
// locked so a concurrent finalization/release serializes behind this transaction.
func (r *DiskQuotaReservationRepository) LockPendingTx(ctx context.Context, tx *gorm.DB, id string) (*models.DiskQuotaReservation, error) {
	if err := requireTx(tx); err != nil {
		return nil, err
	}
	var res models.DiskQuotaReservation
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND status = ?", id, models.DiskQuotaReservationPending).
		First(&res).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDiskReservationNotFound
		}
		return nil, err
	}
	return &res, nil
}

// ConsumeTx atomically flips a pending reservation to consumed and stamps
// consumed_at, using a CONDITIONAL update (WHERE status = 'pending') that REQUIRES
// RowsAffected == 1. It fails with ErrDiskReservationNotFound when the row is
// missing and ErrDiskReservationConflict when it is not pending (already consumed).
// This is the RAW conditional consume used internally by FinalizeTx. It must NOT be
// exposed as a standalone safe attach finalization (see ErrDiskReservationFinalizationRequired).
func (r *DiskQuotaReservationRepository) ConsumeTx(ctx context.Context, tx *gorm.DB, id string) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	now := time.Now()
	res := tx.WithContext(ctx).Model(&models.DiskQuotaReservation{}).
		Where("id = ? AND status = ?", id, models.DiskQuotaReservationPending).
		Updates(map[string]interface{}{
			"status":      models.DiskQuotaReservationConsumed,
			"consumed_at": now,
			"updated_at":  now,
		})
	if res.Error != nil {
		return res.Error
	}
	switch res.RowsAffected {
	case 1:
		return nil
	case 0:
		// Determine whether the row is missing entirely or merely not pending so
		// the caller gets the precise sentinel.
		var exists int64
		if cerr := tx.WithContext(ctx).Model(&models.DiskQuotaReservation{}).
			Where("id = ?", id).Count(&exists).Error; cerr != nil {
			return cerr
		}
		if exists == 0 {
			return ErrDiskReservationNotFound
		}
		return ErrDiskReservationConflict
	default:
		// Exactly one row must be affected; more than one means the unique id
		// predicate matched multiple rows, which is impossible — fail closed.
		return ErrDiskReservationConflict
	}
}

// ReleaseTx hard-deletes a SPECIFIED pending reservation within the caller's
// transaction. It is a hard (physical) delete, NOT a soft delete, and it targets
// exactly one row via a CONDITIONAL delete (WHERE id AND status = 'pending') that
// REQUIRES RowsAffected == 1. Returns ErrDiskReservationNotFound when the row is
// missing or already consumed.
//
// IMPORTANT: ReleaseTx releases ONLY the one reservation identified by id. It does
// NOT bulk-release every reservation for a VM. A normal disk detach must NOT call
// this for unrelated reservations; bulk release is reserved for VM teardown via
// DeleteByVMIDTx. Releasing an already-consumed reservation is a no-op for
// accounting (the vm_disks row carries the real usage) and is reported as
// ErrDiskReservationNotFound.
func (r *DiskQuotaReservationRepository) ReleaseTx(ctx context.Context, tx *gorm.DB, id string) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	res := tx.WithContext(ctx).
		Where("id = ? AND status = ?", id, models.DiskQuotaReservationPending).
		Delete(&models.DiskQuotaReservation{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDiskReservationNotFound
	}
	return nil
}

// DeleteByVMIDTx removes every PENDING reservation for a VM within the caller's
// transaction. It is intended ONLY for terminal VM teardown (invoked via
// QuotaService.DeleteDiskReservationsByVMTx) under a verified lifecycle
// precondition — NOT for ordinary disk detach. Consumed reservations are excluded
// (they no longer count). Callers performing an ordinary detach must consume the
// reservation via the canonical FinalizeTx, not bulk-clear it.
func (r *DiskQuotaReservationRepository) DeleteByVMIDTx(ctx context.Context, tx *gorm.DB, vmID string) error {
	if err := requireTx(tx); err != nil {
		return err
	}
	return tx.WithContext(ctx).
		Where("vm_id = ? AND status = ?", vmID, models.DiskQuotaReservationPending).
		Delete(&models.DiskQuotaReservation{}).Error
}

// deleteByVMIDTx is the unexported alias kept for internal callers that need a
// clearly-scoped teardown helper; it delegates to DeleteByVMIDTx.
func (r *DiskQuotaReservationRepository) deleteByVMIDTx(ctx context.Context, tx *gorm.DB, vmID string) error {
	return r.DeleteByVMIDTx(ctx, tx, vmID)
}

// PendingDiskGBTx sums the size_gb of all PENDING reservations for a user inside
// the caller's transaction, using checked int64 arithmetic. This is the
// conservative over-count that serializes concurrent increases and prevents an
// agent-attached disk from bypassing quota. A reservation with a non-positive
// size_gb (invalid inventory) fails closed.
func (r *DiskQuotaReservationRepository) PendingDiskGBTx(ctx context.Context, tx *gorm.DB, userID string) (int64, error) {
	if err := requireTx(tx); err != nil {
		return 0, err
	}
	// Query each pending size_gb individually (ordered) and accumulate in Go with
	// checked int64 arithmetic. A raw SQL SUM over SQLite INTEGER columns would
	// overflow (or be coerced to a float) BEFORE controlled Go logic could run, so
	// we deliberately avoid SUM and fail closed here instead. The accumulation
	// honors the same contract as TotalDiskAccountingTx: a negative inventory value
	// maps to the existing ErrInvalidDiskInventory sentinel and only arithmetic
	// overflow maps to ErrDiskInventoryOverflow. Unrelated DB errors propagate
	// unchanged. No base-DB fallback is used: the supplied tx is authoritative.
	var sizes []int64
	if err := tx.WithContext(ctx).
		Model(&models.DiskQuotaReservation{}).
		Where("user_id = ? AND status = ?", userID, models.DiskQuotaReservationPending).
		Order("created_at ASC, id ASC").
		Pluck("size_gb", &sizes).Error; err != nil {
		return 0, err
	}
	total := int64(0)
	for _, sz := range sizes {
		if sz < 0 {
			// Invalid (negative) inventory fails closed with the existing sentinel.
			return 0, ErrInvalidDiskInventory
		}
		if sz > 0 {
			var ok bool
			total, ok = addChecked64(total, sz)
			if !ok {
				// Arithmetic overflow maps to the dedicated sentinel.
				return 0, ErrDiskInventoryOverflow
			}
		}
	}
	return total, nil
}

// ListPendingTx lists every pending reservation for a user inside the caller's
// transaction (used for accounting/audit and to verify the one-pending-per-VM
// invariant holds for a given user).
func (r *DiskQuotaReservationRepository) ListPendingTx(ctx context.Context, tx *gorm.DB, userID string) ([]models.DiskQuotaReservation, error) {
	if err := requireTx(tx); err != nil {
		return nil, err
	}
	var out []models.DiskQuotaReservation
	if err := tx.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, models.DiskQuotaReservationPending).
		Order("created_at ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// FinalizeTx is the inner core of the canonical safe finalization. Inside the
// caller's transaction it:
//  1. locks the pending reservation identified by id (FOR UPDATE) and derives the
//     authoritative vm_id and size_gb from the LOCKED row (never caller-controlled);
//  2. creates exactly one VMDisk row using those derived values plus ONLY the
//     non-quota agent output supplied on disk (device, path; lifecycle defaults to
//     'attached'); and
//  3. consumes exactly that one reservation via a conditional update (RowsAffected==1).
//
// The caller (QuotaService.LockAndFinalizeReservationTx) is responsible for having
// already taken the per-user admission lock and revalidated the authoritative
// quota_mode and VM ownership BEFORE calling this core. Because the disk row and the
// reservation consume share one transaction and the disk size is derived from the
// locked reservation, a concurrent admission representation swap cannot undercount.
// If any step conflicts/missing/invalid, it returns a typed error and leaves the
// reservation pending (fail closed).
func (r *DiskQuotaReservationRepository) FinalizeTx(ctx context.Context, tx *gorm.DB, id string, disk *models.VMDisk) (*models.DiskQuotaReservation, error) {
	if err := requireTx(tx); err != nil {
		return nil, err
	}
	locked, err := r.LockPendingTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if disk == nil {
		return nil, ErrDiskReservationConflict
	}
	// The agent-attached device/path are REQUIRED non-quota outputs: a blank or
	// whitespace-only device or path is invalid and must fail closed BEFORE we
	// create the VMDisk (a disk with no backing volume cannot be accounitng-clean).
	if strings.TrimSpace(disk.Device) == "" || strings.TrimSpace(disk.Path) == "" {
		return nil, ErrDiskReservationConflict
	}
	// Derive authoritative values from the locked reservation; reject any caller
	// attempt to override them (VMDisk has no UserID field).
	if disk.VMID != "" && disk.VMID != locked.VMID {
		return nil, ErrDiskReservationConflict
	}
	if disk.SizeGB != 0 && disk.SizeGB != locked.SizeGB {
		return nil, ErrDiskReservationConflict
	}
	disk.VMID = locked.VMID
	disk.SizeGB = locked.SizeGB
	if disk.Lifecycle == "" {
		disk.Lifecycle = models.VMDiskLifecycleAttached
	}
	if disk.SizeGB <= 0 {
		return nil, ErrDiskReservationConflict
	}
	if err := tx.WithContext(ctx).Create(disk).Error; err != nil {
		return nil, err
	}
	if err := r.ConsumeTx(ctx, tx, id); err != nil {
		return nil, err
	}
	return locked, nil
}
