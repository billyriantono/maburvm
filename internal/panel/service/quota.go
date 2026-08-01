package service

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ErrQuotaNegative is the service-level sentinel for a direct legacy quota write
// that carries a negative limit. A negative limit is INVALID and must NEVER be
// interpreted as unlimited; the handler maps this to HTTP 400.
var ErrQuotaNegative = errors.New("quota limits must be non-negative")

// ErrDiskQuotaExceeded is returned when reserving capacity for an extra disk
// would push the user's disk usage (boot + active extra disks + pending
// reservations) past a non-zero disk limit. It is a distinct, disk-only error so
// the handler/agent flow can surface attach-time quota rejection without leaking
// policy/cap detail or collapsing to a generic 500.
var ErrDiskQuotaExceeded = errors.New("disk quota exceeded")

// ErrQuotaExceeded is returned when creating/resizing a VM would exceed a user's quota.
var ErrQuotaExceeded = errors.New("quota exceeded")

// ErrQuotaNotAvailable is returned for managed users when no usable quota can be
// resolved (missing quota row, invalid mode/provenance, or no finite positive
// limits). Managed accounts fail closed; this is distinct from ErrQuotaExceeded
// because nothing was successfully allocated/checked — enrollment/VM actions are
// simply not permitted until an admin assigns a valid managed quota.
var ErrQuotaNotAvailable = errors.New("managed quota not available")

// ErrQuotaAdmissionTransactionRequired is returned by the quarantined standalone
// admission prechecks (AdmitVMCreate/AdmitVMResize). Those methods open and commit
// their OWN transaction and therefore CANNOT serialize with the caller's later
// persistence of the VM row — so they are NOT an allocation authority. Lane D must
// instead run AcquireUserAdmitLockTx + ResolveQuotaTx + ComputeUsageTx/
// TotalDiskAccountingTx inside the SAME caller transaction that persists the VM
// resources. Until Lane D is implemented, the runtime resource-increasing path
// must fail closed rather than trust a detached precheck.
var ErrQuotaAdmissionTransactionRequired = errors.New("admission requires a caller-supplied transaction (Lane D): AdmitVMCreate/AdmitVMResize are not allocation authorities")

// ErrQuotaAdmissionWiringRequired is a redundant alias for the same quarantine
// condition, kept so the message is explicit at each call site.
var ErrQuotaAdmissionWiringRequired = ErrQuotaAdmissionTransactionRequired

// QuotaService enforces per-user resource limits.
type QuotaService struct {
	repo     *repository.QuotaRepository
	userRepo *repository.UserRepository
	vmRepo   *repository.VMRepository
	resRepo  *repository.DiskQuotaReservationRepository
	db       *gorm.DB
}

// NewQuotaService creates a new QuotaService.
func NewQuotaService(db *gorm.DB, vmRepo *repository.VMRepository) *QuotaService {
	return &QuotaService{
		repo:     repository.NewQuotaRepository(db),
		userRepo: repository.NewUserRepository(db),
		vmRepo:   vmRepo,
		resRepo:  repository.NewDiskQuotaReservationRepository(db),
		db:       db,
	}
}

// QuotaUsage is a user's current consumption across all their VMs.
type QuotaUsage struct {
	VMs    int `json:"vms"`
	VCPU   int `json:"vcpu"`
	RAMMB  int `json:"ram_mb"`
	DiskGB int `json:"disk_gb"`
}

// SetQuotaRequest is the admin input for setting a user's quota. Zero = unlimited.
type SetQuotaRequest struct {
	MaxVMs    int `json:"max_vms" validate:"min=0"`
	MaxVCPU   int `json:"max_vcpu" validate:"min=0"`
	MaxRAMMB  int `json:"max_ram_mb" validate:"min=0"`
	MaxDiskGB int `json:"max_disk_gb" validate:"min=0"`
}

// QuotaStatus bundles a user's limits with their current usage for display.
type QuotaStatus struct {
	Quota models.UserQuota `json:"quota"`
	Usage QuotaUsage       `json:"usage"`
}

// GetQuota returns a user's quota. The authoritative users.quota_mode is ALWAYS
// loaded first, even when a quota row exists, so a stale row whose own mode column
// disagrees with the user marker can never override the user-level truth.
//
// The authoritative user marker governs:
//   - managed marker: requires a quota row whose OWN mode is 'managed' AND carries
//     complete usable provenance and strictly positive limits (managedQuotaUsable).
//     A missing row, a malformed snapshot, or a row whose mode disagrees with the
//     marker (e.g. a stray legacy row, or any non-managed row) fails closed with
//     ErrQuotaNotAvailable so a managed account can never silently fall back to
//     unlimited.
//   - legacy marker: a missing row means all-unlimited. A row whose mode is 'legacy'
//     is used as-is (legacy zero = unlimited). A row whose mode disagrees with the
//     legacy marker (a stray managed row) is a marker/row mismatch and fails closed
//     with ErrQuotaNotAvailable — a complete stray managed row must never be let
//     through for a legacy user.
func (s *QuotaService) GetQuota(ctx context.Context, userID string) (*models.UserQuota, error) {
	// Public read path uses the base DB (a real transaction opened here). The
	// authoritative users.quota_mode governs the entire resolution.
	return s.resolveQuotaCore(ctx, s.db, userID)
}

// managedQuotaUsable reports whether a managed quota row carries complete,
// usable provenance and finite positive limits. Without this, a managed account
// with a stray or partially-populated snapshot must not be treated as available.
// It requires: the row's OWN mode is 'managed' (so a stray managed-looking field
// set on a legacy row is never treated as usable), policy id, policy version,
// policy name, policy assigned timestamp, cap revision id, and all four strictly
// positive limits.
func managedQuotaUsable(q *models.UserQuota) bool {
	if q.QuotaMode != models.QuotaModeManaged {
		return false
	}
	if q.PolicyID == nil || q.PolicyVersion == nil || q.PolicyName == nil || q.PolicyAssignedAt == nil || q.CapRevisionID == nil {
		return false
	}
	if q.MaxVMs <= 0 || q.MaxVCPU <= 0 || q.MaxRAMMB <= 0 || q.MaxDiskGB <= 0 {
		return false
	}
	return true
}

// SetQuota creates or updates a user's quota. A direct (legacy) write is rejected
// with ErrManagedQuotaDirectMutation when the target user's authoritative
// users.quota_mode is 'managed' — managed accounts are provisioned exclusively
// via the repository's AssignToUserQuotaTx (preserving full cap-bound provenance).
// Legacy users keep their original direct-write semantics, including zero = unlimited.
//
// A NEGATIVE limit is rejected (ErrQuotaNegative) at this layer (and again in the
// repository as a backstop). Zero is the explicit unlimited sentinel; a negative
// value is never silently treated as unlimited, per the Gate-1 binding requirement.
func (s *QuotaService) SetQuota(ctx context.Context, userID string, req *SetQuotaRequest) (*models.UserQuota, error) {
	if req.MaxVMs < 0 || req.MaxVCPU < 0 || req.MaxRAMMB < 0 || req.MaxDiskGB < 0 {
		return nil, ErrQuotaNegative
	}
	q := &models.UserQuota{
		UserID:    userID,
		MaxVMs:    req.MaxVMs,
		MaxVCPU:   req.MaxVCPU,
		MaxRAMMB:  req.MaxRAMMB,
		MaxDiskGB: req.MaxDiskGB,
	}
	if err := s.repo.Upsert(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// GetUsage computes a user's current resource consumption from their VMs using
// the SINGLE authoritative transaction-scoped accounting source (ComputeUsageTx),
// so status and admission never diverge. A soft-deleted VM remains counted until
// its physical hard deletion (Lane E); boot disks are counted exactly once via the
// shared disk total.
func (s *QuotaService) GetUsage(ctx context.Context, userID string) (QuotaUsage, error) {
	var usage QuotaUsage
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tot, terr := s.repo.ComputeUsageTx(ctx, tx, userID, s.resRepo.WithDB(tx))
		if terr != nil {
			return terr
		}
		// Checked int64->int conversion: overflow fails closed (no clamping).
		v, verr := checkedInt64ToInt(tot.VCPU, repository.ErrResourceOverflow)
		if verr != nil {
			return verr
		}
		r, rerr := checkedInt64ToInt(tot.RAMMB, repository.ErrResourceOverflow)
		if rerr != nil {
			return rerr
		}
		d, derr := checkedInt64ToInt(tot.DiskGB, repository.ErrDiskInventoryOverflow)
		if derr != nil {
			return derr
		}
		usage = QuotaUsage{
			VMs:    tot.VMs,
			VCPU:   v,
			RAMMB:  r,
			DiskGB: d,
		}
		return nil
	})
	if err != nil {
		return QuotaUsage{}, err
	}
	return usage, nil
}

// GetDiskUsage computes the user's CURRENT disk consumption, INCLUDING pending
// disk-quota reservations, for display/status purposes. It uses the SAME
// authoritative total source (TotalDiskAccountingTx, inside a transaction) as
// admission so status and admission never diverge. A zero disk limit means unlimited.
func (s *QuotaService) GetDiskUsage(ctx context.Context, userID string) (int, error) {
	var total int
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, terr := s.repo.TotalDiskAccountingTx(ctx, tx, userID, s.resRepo.WithDB(tx))
		if terr != nil {
			return terr
		}
		// Checked int64->int conversion: overflow fails closed (no clamping).
		c, cerr := checkedInt64ToInt(t, repository.ErrDiskInventoryOverflow)
		if cerr != nil {
			return cerr
		}
		total = c
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// ReserveDiskQuota evaluates the user's disk quota (boot disks + attached & deleting
// extra disks + pending reservations, read under the per-user admission advisory
// lock) and, if the requested extra disk fits, inserts a PENDING disk-quota
// reservation and returns it. The caller must drive the agent AttachDisk RPC next;
// on failure it releases the reservation (ReleaseDiskReservation), and on success
// it finalizes via LockAndFinalizeReservationTx inside the SAME transaction that
// records the vm_disks row. Raw ConsumeDiskReservationTx is NOT a safe finalization.
//
// A negative sizeGB is rejected (an extra disk must be strictly positive). A zero
// disk limit means unlimited, so only a positive, exceeded limit is rejected.
//
// If the user's effective quota row cannot be resolved (managed pending / missing)
// for a managed account, the disk admission fails closed via ErrQuotaNotAvailable
// — the agent must not be driven to attach a disk we cannot account for.
func (s *QuotaService) ReserveDiskQuota(ctx context.Context, userID, vmID string, sizeGB int) (*models.DiskQuotaReservation, error) {
	if sizeGB <= 0 {
		return nil, fmt.Errorf("disk size must be a positive number of GB")
	}
	return s.reserveDiskQuotaUnderLock(ctx, userID, vmID, sizeGB)
}

// reserveDiskQuotaUnderLock runs the quota read + pending-reservation insert in a
// single transaction, taking the per-user admission advisory lock FIRST (before
// the quota read) so concurrent resource-increasing operations for the same user
// serialize and cannot double-spend capacity. The authoritative quota_mode and row
// are resolved together via the centralized resolveQuotaCore on the SAME
// transaction handle (no base-DB bypass).
func (s *QuotaService) reserveDiskQuotaUnderLock(ctx context.Context, userID, vmID string, sizeGB int) (*models.DiskQuotaReservation, error) {
	var res *models.DiskQuotaReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := repository.AcquireQuotaAdmitLock(tx, userID); err != nil {
			return err
		}
		// Authoritative mode + row resolution in the same supplied tx.
		q, qerr := s.resolveQuotaCore(ctx, tx, userID)
		if qerr != nil {
			return qerr
		}

		used, err := s.repo.TotalDiskAccountingTx(ctx, tx, userID, s.resRepo.WithDB(tx))
		if err != nil {
			return err
		}
		if q.MaxDiskGB > 0 {
			proj, nover := addChecked64Int(used, int64(sizeGB))
			if !nover && used > 0 && sizeGB > 0 {
				return repository.ErrDiskInventoryOverflow
			}
			if proj > int64(q.MaxDiskGB) {
				return fmt.Errorf("%w: disk %d/%d GB (incl. pending reservations)", ErrDiskQuotaExceeded, proj, q.MaxDiskGB)
			}
		}

		res = &models.DiskQuotaReservation{
			UserID: userID,
			VMID:   vmID,
			SizeGB: sizeGB,
			Status: models.DiskQuotaReservationPending,
		}
		return s.resRepo.WithDB(tx).CreateTx(ctx, tx, res)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ReleaseDiskReservation removes a pending reservation (agent attach failed, or
// VM/disk cleanup). It is safe to call only for a pending reservation; a consumed
// reservation is already converted into real disk usage and is not released here.
//
// Lifecycle: a pre-lock peek learns only the owner; after the owner's admission
// advisory lock is taken we RE-LOCK the pending reservation (LockPendingTx) and
// revalidate the authoritative mode/quota row and the VM-owner mapping using the
// RELOADED row, then release EXACTLY that one reservation id. We never bulk
// release.
func (s *QuotaService) ReleaseDiskReservation(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Pre-lock peek: owner only.
		peek, perr := s.resRepo.WithDB(tx).GetPendingTx(ctx, tx, id)
		if perr != nil {
			return perr
		}
		// Owner admission lock.
		if err := repository.AcquireQuotaAdmitLock(tx, peek.UserID); err != nil {
			return err
		}
		// Re-lock the pending reservation under the lock.
		locked, lerr := s.resRepo.WithDB(tx).LockPendingTx(ctx, tx, id)
		if lerr != nil {
			return lerr
		}
		if locked.UserID != peek.UserID {
			return repository.ErrDiskReservationConflict
		}
		// Revalidate authoritative mode/quota row for the reloaded owner.
		if _, qerr := s.resolveQuotaCore(ctx, tx, locked.UserID); qerr != nil {
			return qerr
		}
		// Revalidate VM ownership with the reloaded reservation's VM.
		var vm models.VM
		if err := tx.WithContext(ctx).Unscoped().
			Select("id", "user_id").First(&vm, "id = ?", locked.VMID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return repository.ErrDiskReservationConflict
			}
			return err
		}
		if vm.UserID != locked.UserID {
			return repository.ErrDiskReservationConflict
		}
		// Exact release of this reservation id (no bulk).
		return s.resRepo.WithDB(tx).ReleaseTx(ctx, tx, id)
	})
}

// ConsumeDiskReservationTx is the DEPRECATED raw consume compatibility surface for
// the unmodified vm.go attach path. A safe attach finalization MUST use
// LockAndFinalizeReservationTx (which creates the VMDisk from the locked
// reservation in the same transaction). To keep the current agent-attach path from
// silently performing an unsafe accounting swap, this method fails closed with
// ErrDiskReservationFinalizationRequired, leaving the pending reservation intact
// (fail closed) until Lane D wires the canonical primitive. It is intentionally NOT
// described as a normal attach finalization.
func (s *QuotaService) ConsumeDiskReservationTx(ctx context.Context, tx *gorm.DB, id string) error {
	return repository.ErrDiskReservationFinalizationRequired
}

// DeleteDiskReservationsByVMTx removes all pending reservations for a VM within
// the caller's transaction. It is a terminal VM-TEARDOWN helper (used by VM
// deletion) under a verified lifecycle precondition — NOT ordinary disk detach.
// Ordinary detach must finalize via LockAndFinalizeReservationTx.
func (s *QuotaService) DeleteDiskReservationsByVMTx(ctx context.Context, tx *gorm.DB, vmID string) error {
	return s.resRepo.WithDB(tx).DeleteByVMIDTx(ctx, tx, vmID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Lane D admission primitives (future panel VM-create lane).
//
// These are NOT a pre-commit check API that falsely promises atomicity. They
// expose the building blocks so the caller can, in ONE supplied transaction:
//
//	1. acquire the per-user admission advisory lock (AcquireUserAdmitLockTx);
//	2. resolve the authoritative quota row + mode (ResolveQuotaTx / Mode);
//	3. calculate the authoritative resource total (ComputeUsageTx /
//	   TotalDiskAccountingTx — the single source shared with status/admission);
//	4. persist local accounting (VM row, vm_disks, reservation finalization).
//
// LOCK ORDER (fixed, documented): cap lock (only managed policy
// publication/assignment) -> user quota lock (per-user admission advisory lock) ->
// user row -> VM row -> reservation/disk. The cap lock is taken by managed policy
// publication/assignment only; ordinary VM/disk admission starts at the per-user
// quota lock.
// ─────────────────────────────────────────────────────────────────────────────

// AcquireUserAdmitLockTx takes the per-user admission advisory lock inside the
// caller's transaction. Distinct users admit in parallel; the same user serializes.
// On PostgreSQL this is a transaction-scoped advisory lock; on SQLite (unit tests)
// it is a no-op and serialization is provided by the surrounding single-writer
// transaction. Must be called as the FIRST step of an admission transaction so
// concurrent resource-increasing operations for the same user cannot double-spend.
func (s *QuotaService) AcquireUserAdmitLockTx(ctx context.Context, tx *gorm.DB, userID string) error {
	return repository.AcquireQuotaAdmitLock(tx, userID)
}

// ResolveQuotaModeTx returns the authoritative quota_mode inside the supplied
// transaction, with a row lock, after the per-user admission lock has been taken.
func (s *QuotaService) ResolveQuotaModeTx(ctx context.Context, tx *gorm.DB, userID string) (models.QuotaMode, error) {
	return s.repo.GetUserQuotaModeTx(ctx, tx, userID)
}

// ResolveQuotaTx resolves the effective quota row inside a transaction, honoring
// the authoritative users.quota_mode: legacy users with no row are unlimited;
// managed users without a usable row fail closed (ErrQuotaNotAvailable). The
// authoritative mode is read in the same tx (GetUserQuotaModeTx), never the base DB.
func (s *QuotaService) ResolveQuotaTx(ctx context.Context, tx *gorm.DB, userID string) (*models.UserQuota, error) {
	return s.resolveQuotaTx(ctx, tx, userID)
}

// ComputeUsageTx returns the authoritative resource usage totals inside the
// caller's transaction (VM count + vCPU + RAM + disk), using the single shared
// accounting source. Used by Lane D admission and by status.
func (s *QuotaService) ComputeUsageTx(ctx context.Context, tx *gorm.DB, userID string) (QuotaUsage, error) {
	tot, err := s.repo.ComputeUsageTx(ctx, tx, userID, s.resRepo.WithDB(tx))
	if err != nil {
		return QuotaUsage{}, err
	}
	// Checked int64->int conversion: overflow fails closed (no clamping).
	v, verr := checkedInt64ToInt(tot.VCPU, repository.ErrResourceOverflow)
	if verr != nil {
		return QuotaUsage{}, verr
	}
	r, rerr := checkedInt64ToInt(tot.RAMMB, repository.ErrResourceOverflow)
	if rerr != nil {
		return QuotaUsage{}, rerr
	}
	d, derr := checkedInt64ToInt(tot.DiskGB, repository.ErrDiskInventoryOverflow)
	if derr != nil {
		return QuotaUsage{}, derr
	}
	return QuotaUsage{
		VMs:    tot.VMs,
		VCPU:   v,
		RAMMB:  r,
		DiskGB: d,
	}, nil
}

// TotalDiskAccountingTx returns the authoritative disk total for a user inside the
// caller's transaction (boot disks + attached & deleting extra disks + pending
// reservations), computed with checked int64 arithmetic. Future VM/worker lanes use
// this same source so admission and status can never diverge.
func (s *QuotaService) TotalDiskAccountingTx(ctx context.Context, tx *gorm.DB, userID string) (int64, error) {
	return s.repo.TotalDiskAccountingTx(ctx, tx, userID, s.resRepo.WithDB(tx))
}

// LockAndFinalizeReservationTx is the CANONICAL safe finalization primitive for
// Lane D/E. Inside the caller's transaction it:
//  1. takes the per-user admission lock for the reservation's owner (resolved from
//     the reservation itself, since the caller only knows the reservation id);
//  2. reads the authoritative quota_mode in the same tx and revalidates ownership
//     (the locked reservation's vm_id must belong to the lock owner);
//  3. delegates to the repository core which locks the pending reservation, derives
//     ONLY vm_id + size_gb from the locked row, creates exactly one VMDisk from those
//     derived values plus ONLY the non-quota agent output on disk (device, path;
//     lifecycle defaults to 'attached'), and consumes exactly that reservation.
//
// The caller must supply a non-nil transaction and a disk carrying at most
// agent output; any attempt to override vm_id/size_gb fails closed. If a conflict or
// invalid state occurs, the reservation is left pending (fail closed) and a typed
// error is returned.
func (s *QuotaService) LockAndFinalizeReservationTx(ctx context.Context, tx *gorm.DB, id string, disk *models.VMDisk) (*models.DiskQuotaReservation, error) {
	if tx == nil {
		return nil, repository.ErrQuotaTxRequired
	}
	// STEP 1: pre-lock peek may ONLY learn the owner (a non-locking read). We must
	// NOT trust peek's VM/quota for finalization — that would be a representation
	// gap. A concurrent finalization cannot have consumed it yet (still pending).
	peek, perr := s.resRepo.WithDB(tx).GetPendingTx(ctx, tx, id)
	if perr != nil {
		return nil, perr
	}
	// STEP 2: take the per-user admission lock for the reservation owner, then
	// RE-LOCK the pending reservation and validate everything from the RELOADED
	// row inside the same locked transaction.
	if err := repository.AcquireQuotaAdmitLock(tx, peek.UserID); err != nil {
		return nil, err
	}

	// Re-lock the reservation NOW (under the owner admission lock) and derive the
	// authoritative owner/VM/size from the reloaded row.
	locked, lerr := s.resRepo.WithDB(tx).LockPendingTx(ctx, tx, id)
	if lerr != nil {
		return nil, lerr
	}
	if locked.UserID != peek.UserID {
		// Owner changed between peek and lock: representation gap, fail closed.
		return nil, repository.ErrDiskReservationConflict
	}

	// Revalidate the authoritative quota mode/row for the reloaded owner.
	if _, qerr := s.resolveQuotaCore(ctx, tx, locked.UserID); qerr != nil {
		return nil, qerr
	}

	// Revalidate VM ownership with the reloaded owner and VM. The repository core
	// will re-derive vm_id/size_gb from its own lock, but we fail closed here on
	// any owner/VM map mismatch before delegating.
	var vm models.VM
	if err := tx.WithContext(ctx).Unscoped().
		Select("id", "user_id").First(&vm, "id = ?", locked.VMID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrDiskReservationConflict
		}
		return nil, err
	}
	if vm.UserID != locked.UserID {
		return nil, repository.ErrDiskReservationConflict
	}

	// STEP 3: canonical core uses ONLY the reloaded reservation authority for
	// vm_id/size_gb; it independently rejects blank device/path and overrides.
	return s.resRepo.WithDB(tx).FinalizeTx(ctx, tx, id, disk)
}

// ReserveDiskQuotaTx is the transaction-scoped admission primitive for future
// panel Lane D: it evaluates the user's disk quota (boot disks + attached &
// deleting extra disks + pending reservations, read under the per-user admission
// advisory lock) and, if the requested extra disk fits, inserts a PENDING disk-
// quota reservation and returns it. The caller drives the agent AttachDisk RPC and
// then finalizes with LockAndFinalizeReservationTx inside the SAME transaction that
// records the vm_disks row.
//
// NOTE: this is a compatibility shim. The preferred path is to open the transaction
// in the caller and call AcquireUserAdmitLockTx + ResolveQuotaTx + TotalDiskAccountingTx
// + CreateTx directly. This method remains to avoid breaking existing callers; it is
// explicitly NON-AUTHORITATIVE for finalization (it does not consume the reservation).
func (s *QuotaService) ReserveDiskQuotaTx(ctx context.Context, tx *gorm.DB, userID, vmID string, sizeGB int) (*models.DiskQuotaReservation, error) {
	if sizeGB <= 0 {
		return nil, fmt.Errorf("disk size must be a positive number of GB")
	}
	if err := repository.AcquireQuotaAdmitLock(tx, userID); err != nil {
		return nil, err
	}
	// Authoritative mode + row resolution in the same supplied tx.
	q, qerr := s.resolveQuotaCore(ctx, tx, userID)
	if qerr != nil {
		return nil, qerr
	}

	used, err := s.repo.TotalDiskAccountingTx(ctx, tx, userID, s.resRepo.WithDB(tx))
	if err != nil {
		return nil, err
	}
	if q.MaxDiskGB > 0 {
		proj, nover := addChecked64Int(used, int64(sizeGB))
		if !nover && used > 0 && sizeGB > 0 {
			return nil, repository.ErrDiskInventoryOverflow
		}
		if proj > int64(q.MaxDiskGB) {
			return nil, fmt.Errorf("%w: disk %d/%d GB (incl. pending reservations)", ErrDiskQuotaExceeded, proj, q.MaxDiskGB)
		}
	}

	res := &models.DiskQuotaReservation{
		UserID: userID,
		VMID:   vmID,
		SizeGB: sizeGB,
		Status: models.DiskQuotaReservationPending,
	}
	if err := s.resRepo.WithDB(tx).CreateTx(ctx, tx, res); err != nil {
		return nil, err
	}
	return res, nil
}

// AdmitVMCreate is QUARANTINED. It opens and commits its OWN transaction, so it
// cannot serialize with the caller's later persistence of the VM row — it is NOT an
// allocation authority. The runtime VM-create path must use Lane D transaction
// primitives instead. Until then it fails closed with
// ErrQuotaAdmissionTransactionRequired.
func (s *QuotaService) AdmitVMCreate(ctx context.Context, userID string, add models.Resources) error {
	return ErrQuotaAdmissionTransactionRequired
}

// AdmitVMCreateTx is the canonical Lane D admission for VM creation. Called inside
// the SAME transaction that persists the VM row, it (1) takes the per-user
// admission advisory lock, (2) resolves the authoritative quota + usage in that
// tx, and (3) fails closed with ErrQuotaExceeded if `add` wouldn't fit. Running in
// the caller's tx is what makes it an allocation authority: concurrent same-user
// creates serialize on the lock and cannot double-spend. The VM row must be
// inserted in the same tx AFTER this returns nil.
func (s *QuotaService) AdmitVMCreateTx(ctx context.Context, tx *gorm.DB, userID string, add models.Resources) error {
	if tx == nil {
		return ErrQuotaAdmissionTransactionRequired
	}
	if err := s.AcquireUserAdmitLockTx(ctx, tx, userID); err != nil {
		return err
	}
	q, err := s.ResolveQuotaTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	usage, err := s.ComputeUsageTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	return evaluateQuota(q, usage, add)
}

// AdmitVMResize is QUARANTINED, analogous to AdmitVMCreate: it cannot serialize
// with the caller's later persistence, so it is NOT an allocation authority. Fails
// closed with ErrQuotaAdmissionWiringRequired until Lane D replaces its caller.
func (s *QuotaService) AdmitVMResize(ctx context.Context, userID string, oldRes, newRes models.Resources) error {
	return ErrQuotaAdmissionWiringRequired
}

// AdmitVMResizeTx is the canonical Lane D admission for a resize, called inside the
// tx that persists the new resources. A resize keeps the VM count unchanged, so it
// evaluates the delta (newRes over oldRes, which current usage already includes).
func (s *QuotaService) AdmitVMResizeTx(ctx context.Context, tx *gorm.DB, userID string, oldRes, newRes models.Resources) error {
	if tx == nil {
		return ErrQuotaAdmissionTransactionRequired
	}
	if err := s.AcquireUserAdmitLockTx(ctx, tx, userID); err != nil {
		return err
	}
	q, err := s.ResolveQuotaTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	usage, err := s.ComputeUsageTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	return evaluateTotals(q, QuotaUsage{
		VMs:    usage.VMs,
		VCPU:   usage.VCPU - oldRes.CPU + newRes.CPU,
		RAMMB:  usage.RAMMB - oldRes.RAM + newRes.RAM,
		DiskGB: usage.DiskGB - oldRes.Disk + newRes.Disk,
	})
}

// resolveQuotaCore is the single authoritative resolution used by BOTH the public
// GetQuota path and every transaction path. The authoritative users.quota_mode
// (read from the same DB/tx handle) governs:
//
//   - managed marker: requires exactly a usable managed row (row QuotaMode ==
//     managed AND managedQuotaUsable). Missing row, legacy row, partial/invalid
//     managed row, or ANY non-legacy (incl. a fully complete stray managed) row for
//     a legacy marker => ErrQuotaNotAvailable. A legacy user must never be promoted
//     by a stray managed row, and a managed user must never silently fall back to
//     unlimited.
//   - legacy marker: a missing row is a synthetic legacy zero/unlimited quota. A
//     row whose mode is legacy is used as-is. ANY row whose mode is not 'legacy'
//     (e.g. a complete managed row) is a marker/row mismatch and fails closed.
//
// modeHandle is the gorm handle to read through (the caller's supplied tx for
// transaction paths, or the base db for the public read path). There is never a
// base-DB fallback once a caller has supplied a transaction: the same tx is used
// for both the mode lookup and the row lookup.
func (s *QuotaService) resolveQuotaCore(ctx context.Context, modeHandle *gorm.DB, userID string) (*models.UserQuota, error) {
	mode, merr := s.repo.GetUserQuotaModeOn(ctx, modeHandle, userID)
	if merr != nil {
		return nil, merr
	}

	q, qerr := s.repo.GetByUserIDOn(ctx, modeHandle, userID)
	if qerr != nil {
		if !errors.Is(qerr, gorm.ErrRecordNotFound) {
			return nil, qerr
		}
		// No row at all.
		if mode == models.QuotaModeManaged {
			return nil, ErrQuotaNotAvailable
		}
		// Legacy marker: missing row => synthetic legacy zero/unlimited.
		return &models.UserQuota{UserID: userID}, nil
	}

	if mode == models.QuotaModeManaged {
		// Marker says managed: the row MUST be a usable managed snapshot. ANY
		// non-managed row, a partial/invalid managed row, or missing provenance
		// fails closed. We never infer usability from q.IsManaged() alone.
		if !managedQuotaUsable(q) {
			return nil, ErrQuotaNotAvailable
		}
		return q, nil
	}

	// Marker says legacy: only a legacy row is acceptable. ANY non-legacy row
	// (including a complete managed row) is a marker/row mismatch and fails closed.
	if q.QuotaMode != models.QuotaModeLegacy {
		return nil, ErrQuotaNotAvailable
	}
	return q, nil
}

// resolveQuotaTx is the transaction-scoped wrapper used by Lane D admission and
// the reserve/finalize/release paths. It reads the authoritative mode and row
// inside the supplied tx (no base-DB bypass).
func (s *QuotaService) resolveQuotaTx(ctx context.Context, tx *gorm.DB, userID string) (*models.UserQuota, error) {
	return s.resolveQuotaCore(ctx, tx, userID)
}

// GetStatus returns a user's quota together with current usage, both derived from
// the authoritative transaction-scoped sources so they cannot diverge.
func (s *QuotaService) GetStatus(ctx context.Context, userID string) (*QuotaStatus, error) {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return nil, err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &QuotaStatus{Quota: *q, Usage: usage}, nil
}

// CheckCanCreate returns ErrQuotaExceeded (wrapped with detail) if allocating
// `add` for one new VM would push the user past any non-zero limit. It is a
// NON-AUTHORITATIVE read-only precheck (uses the authoritative usage totals) and
// must not be used as the sole allocation authority at runtime; Lane D replaces
// the caller with transaction-scoped admission.
func (s *QuotaService) CheckCanCreate(ctx context.Context, userID string, add models.Resources) error {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return err
	}
	return evaluateQuota(q, usage, add)
}

// CheckCanResize returns ErrQuotaExceeded if changing one VM's resources from
// oldRes to newRes would push the user past any non-zero limit. A resize leaves
// the VM count unchanged; current usage already includes oldRes. Non-authoritative
// read-only precheck.
func (s *QuotaService) CheckCanResize(ctx context.Context, userID string, oldRes, newRes models.Resources) error {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return err
	}
	after := QuotaUsage{
		VMs:    usage.VMs,
		VCPU:   usage.VCPU - oldRes.CPU + newRes.CPU,
		RAMMB:  usage.RAMMB - oldRes.RAM + newRes.RAM,
		DiskGB: usage.DiskGB - oldRes.Disk + newRes.Disk,
	}
	return evaluateTotals(q, after)
}

// evaluateQuota is the pure create-time core: it checks the effect of adding
// exactly one VM consuming `add` on top of current usage.
func evaluateQuota(q *models.UserQuota, used QuotaUsage, add models.Resources) error {
	return evaluateTotals(q, QuotaUsage{
		VMs:    used.VMs + 1,
		VCPU:   used.VCPU + add.CPU,
		RAMMB:  used.RAMMB + add.RAM,
		DiskGB: used.DiskGB + add.Disk,
	})
}

// evaluateTotals is the pure limit-checking core (no I/O): it rejects when any
// projected total exceeds its corresponding non-zero limit. A zero limit means
// unlimited.
func evaluateTotals(q *models.UserQuota, t QuotaUsage) error {
	switch {
	case q.MaxVMs > 0 && t.VMs > q.MaxVMs:
		return fmt.Errorf("%w: VM count %d/%d", ErrQuotaExceeded, t.VMs, q.MaxVMs)
	case q.MaxVCPU > 0 && t.VCPU > q.MaxVCPU:
		return fmt.Errorf("%w: vCPU %d/%d", ErrQuotaExceeded, t.VCPU, q.MaxVCPU)
	case q.MaxRAMMB > 0 && t.RAMMB > q.MaxRAMMB:
		return fmt.Errorf("%w: RAM %d/%d MB", ErrQuotaExceeded, t.RAMMB, q.MaxRAMMB)
	case q.MaxDiskGB > 0 && t.DiskGB > q.MaxDiskGB:
		return fmt.Errorf("%w: disk %d/%d GB", ErrQuotaExceeded, t.DiskGB, q.MaxDiskGB)
	}
	return nil
}

// addChecked64Int adds two int64 values and reports overflow (mirrors the
// repository helper for service-layer projection checks).
func addChecked64Int(a, b int64) (int64, bool) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum > 0) {
		return 0, false
	}
	return sum, true
}

// checkedInt64ToInt converts an int64 to int. On 32-bit or any architecture where
// the value exceeds the platform int range, it returns an error (architecture
// overflow detected) and NEVER clamps — clamping would silently cap accounting and
// let admission/status disagree, so failure is the only safe behavior. On 64-bit
// platforms int == int64 and this cannot overflow, but the guard stays correct
// everywhere.
func checkedInt64ToInt(v int64, overflowErr error) (int, error) {
	if v > int64(math.MaxInt) || v < int64(math.MinInt) {
		return 0, overflowErr
	}
	return int(v), nil
}
