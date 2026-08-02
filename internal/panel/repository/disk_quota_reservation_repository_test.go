package repository

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// diskResSchema is a minimal SQLite mirror sufficient to exercise the reservation
// repository and DiskUsageTx (boot disks from vms + active vm_disks + pending
// reservations) without a live PostgreSQL.
const diskResSchema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '', email TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role TEXT DEFAULT 'client',
	two_factor_secret TEXT,
	two_factor_backup_codes TEXT,
	ip_whitelist TEXT,
	quota_mode TEXT NOT NULL DEFAULT 'legacy',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	token_revoked_at DATETIME, deleted_at DATETIME
);
CREATE TABLE IF NOT EXISTS user_quotas (
	user_id TEXT PRIMARY KEY,
	max_vms INTEGER NOT NULL DEFAULT 0,
	max_vcpu INTEGER NOT NULL DEFAULT 0,
	max_ram_mb INTEGER NOT NULL DEFAULT 0,
	max_disk_gb INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	quota_mode TEXT NOT NULL DEFAULT 'legacy',
	policy_id TEXT,
	policy_version INTEGER,
	policy_name TEXT,
	policy_assigned_at DATETIME,
	policy_assigned_by TEXT,
	cap_revision_id TEXT
);
CREATE TABLE IF NOT EXISTS vms (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	hostname TEXT NOT NULL,
	os_template_id TEXT NOT NULL,
	resources TEXT,
	status TEXT DEFAULT 'stopped',
	source_migration TEXT DEFAULT NULL,
	vnc_port INTEGER,
	vnc_password TEXT,
	console_enabled BOOLEAN NOT NULL DEFAULT 1,
	rescue_mode BOOLEAN NOT NULL DEFAULT 0,
	created_at DATETIME,
	updated_at DATETIME,
	deleted_at DATETIME
);
CREATE TABLE IF NOT EXISTS vm_disks (
	id TEXT PRIMARY KEY,
	vm_id TEXT NOT NULL,
	device TEXT NOT NULL,
	size_gb INTEGER NOT NULL,
	path TEXT NOT NULL,
	lifecycle TEXT NOT NULL DEFAULT 'attached',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	deleted_at DATETIME
);
CREATE TABLE IF NOT EXISTS disk_quota_reservations (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	vm_id TEXT NOT NULL,
	size_gb INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	consumed_at DATETIME
);
CREATE UNIQUE INDEX IF NOT EXISTS disk_res_one_pending_per_vm ON disk_quota_reservations(vm_id) WHERE status = 'pending';
`

func newDiskResTestDB(t *testing.T) (*gorm.DB, *DiskQuotaReservationRepository, *QuotaRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:diskres_"+uuid.NewString()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(diskResSchema).Error)
	return db, NewDiskQuotaReservationRepository(db), NewQuotaRepository(db)
}

func seedDiskResUser(t *testing.T, db *gorm.DB, mode models.QuotaMode) string {
	t.Helper()
	u := &models.User{Email: "disk-" + uuid.NewString() + "@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: mode}
	require.NoError(t, db.Create(u).Error)
	return u.ID.String()
}

func seedDiskResVM(t *testing.T, db *gorm.DB, userID string) string {
	t.Helper()
	vm := &models.VM{
		UserID:       userID,
		NodeID:       uuid.NewString(),
		Hostname:     "h" + uuid.NewString(),
		OSTemplateID: uuid.NewString(),
		Resources:    models.Resources{CPU: 1, RAM: 1024, Disk: 20},
		Status:       models.VMStatusRunning,
	}
	require.NoError(t, db.Create(vm).Error)
	return vm.ID
}

// Reservation lifecycle: create (pending) -> consume flips status and stamps
// consumed_at; a second consume is a conflict; release of a consumed one fails.
func TestDiskReservationLifecycle(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid)

	res := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 10}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res))
	require.Equal(t, models.DiskQuotaReservationPending, res.Status)

	// Consume requires exactly one pending row (RowsAffected == 1).
	require.NoError(t, resRepo.WithDB(db).ConsumeTx(ctx, db, res.ID))
	consumed, err := resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.ErrorIs(t, err, ErrDiskReservationNotFound)
	require.Nil(t, consumed)

	// Re-consuming a consumed reservation is a conflict (conditional update
	// matched 0 rows; the row still exists, so it is NOT a not-found).
	require.ErrorIs(t, resRepo.WithDB(db).ConsumeTx(ctx, db, res.ID), ErrDiskReservationConflict)

	// Consuming a never-existent reservation is a not-found.
	require.ErrorIs(t, resRepo.WithDB(db).ConsumeTx(ctx, db, uuid.NewString()), ErrDiskReservationNotFound)
}

// Release only works on a pending reservation; releasing a consumed one errors.
func TestDiskReservationReleaseOnlyPending(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid)

	res := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 10}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res))
	require.NoError(t, resRepo.WithDB(db).ReleaseTx(ctx, db, res.ID))
	_, err := resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.ErrorIs(t, err, ErrDiskReservationNotFound)

	// A consumed reservation cannot be released (it is no longer pending).
	res2 := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 5}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res2))
	require.NoError(t, resRepo.WithDB(db).ConsumeTx(ctx, db, res2.ID))
	require.ErrorIs(t, resRepo.WithDB(db).ReleaseTx(ctx, db, res2.ID), ErrDiskReservationNotFound)
}

// DiskUsageTx sums boot disks + active extra disks (attached AND deleting) +
// pending reservations, and excludes soft-deleted VMs/disks and consumed
// reservations.
func TestDiskUsageTxComponents(t *testing.T) {
	ctx := context.Background()
	db, resRepo, qRepo := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid) // boot disk 20GB

	// Add an attached extra disk of 5GB.
	require.NoError(t, db.Create(&models.VMDisk{VMID: vmID, Device: "vdb", SizeGB: 5, Path: "/p", Lifecycle: models.VMDiskLifecycleAttached}).Error)
	// Add a deleting extra disk of 7GB — still physically allocated, must COUNT.
	require.NoError(t, db.Create(&models.VMDisk{VMID: vmID, Device: "vdc", SizeGB: 7, Path: "/p2", Lifecycle: models.VMDiskLifecycleDeleting}).Error)
	// A pending reservation of 8GB must over-count.
	res := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 8}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res))

	used, err := qRepo.DiskUsageTx(ctx, db, uid, resRepo)
	require.NoError(t, err)
	assert.Equal(t, 20+5+7+8, used)

	// Consuming the reservation removes it from the sum (attached+deleting remain).
	require.NoError(t, resRepo.WithDB(db).ConsumeTx(ctx, db, res.ID))
	used2, err := qRepo.DiskUsageTx(ctx, db, uid, resRepo)
	require.NoError(t, err)
	assert.Equal(t, 20+5+7, used2)

	// Soft-deleting a VM (GORM sets deleted_at) does NOT drop it from the sum:
	// a soft-deleted VM remains physically present and is still quota-counted
	// until its physical hard deletion (Lane E). Boot + attached + deleting stay.
	require.NoError(t, db.Delete(&models.VM{ID: vmID}).Error)
	used3, err := qRepo.DiskUsageTx(ctx, db, uid, resRepo)
	require.NoError(t, err)
	assert.Equal(t, 20+5+7, used3)

	// Only a HARD delete removes the VM from accounting. We emulate it by
	// physically deleting the row (bypassing GORM soft-delete) and its disks.
	require.NoError(t, db.Unscoped().Where("id = ?", vmID).Delete(&models.VM{}).Error)
	require.NoError(t, db.Unscoped().Where("vm_id = ?", vmID).Delete(&models.VMDisk{}).Error)
	used4, err := qRepo.DiskUsageTx(ctx, db, uid, resRepo)
	require.NoError(t, err)
	assert.Equal(t, 0, used4)
}

// TestDiskUsageTxOverflowFailsClosed proves the accounting source fails CLOSED on
// int64 overflow rather than wrapping. We inject two pending reservations whose
// sizes sum past math.MaxInt64 via raw SQL (size_gb is a 64-bit INTEGER column, so
// values near the int64 ceiling are representable), then assert the total returns
// ErrDiskInventoryOverflow.
func TestDiskUsageTxOverflowFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, resRepo, qRepo := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	// Two DISTINCT valid VMs for the same user so the one-pending-per-VM unique
	// index is honored (the overflow must come from the summed size_gb, not from a
	// constraint violation).
	vm1 := seedDiskResVM(t, db, uid)
	vm2 := seedDiskResVM(t, db, uid)

	half := (math.MaxInt64 / 2) + 1
	// Two pending reservations whose sum overflows int64. We bind the sizes via
	// raw SQL (no models.Resources SQL binding) and use SQLite CURRENT_TIMESTAMP.
	require.NoError(t, db.Exec(
		"INSERT INTO disk_quota_reservations (id, user_id, vm_id, size_gb, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		uuid.NewString(), uid, vm1, half).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO disk_quota_reservations (id, user_id, vm_id, size_gb, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		uuid.NewString(), uid, vm2, half).Error)

	_, err := qRepo.TotalDiskAccountingTx(ctx, db, uid, resRepo)
	require.ErrorIs(t, err, ErrDiskInventoryOverflow)
}

// TestReservationFinalizeDerivesFromLockedRow proves the finalization primitive
// locks the pending reservation and returns the authoritative user/vm/size from the
// locked row — NOT any caller-supplied values — and consumes exactly that one
// reservation (RowsAffected == 1, removed from the pending sum).
func TestReservationFinalizeDerivesFromLockedRow(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid)

	res := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 42}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res))

	// A caller attempting to finalize with its own (wrong) size must NOT be able
	// to influence the result: FinalizeTx derives size from the locked row. The
	// caller may pass only non-quota agent output (device/path).
	disk := &models.VMDisk{Device: "vdb", Path: "/disk/42"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		got, ferr := resRepo.WithDB(tx).FinalizeTx(ctx, tx, res.ID, disk)
		if ferr != nil {
			return ferr
		}
		assert.Equal(t, uid, got.UserID)
		assert.Equal(t, vmID, got.VMID)
		assert.Equal(t, 42, got.SizeGB) // authoritative, from locked row
		return nil
	}))

	// A VMDisk row was created from the locked reservation (size derived, not from
	// caller). No representation gap: the disk is now counted and the reservation
	// is consumed in the same transaction.
	var created models.VMDisk
	require.NoError(t, db.First(&created, "vm_id = ?", vmID).Error)
	assert.Equal(t, 42, created.SizeGB)
	assert.Equal(t, models.VMDiskLifecycleAttached, created.Lifecycle)

	// The reservation is now consumed: no longer pending, no longer in the sum.
	_, err := resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.ErrorIs(t, err, ErrDiskReservationNotFound)
	sum, err := resRepo.WithDB(db).PendingDiskGBTx(ctx, db, uid)
	require.NoError(t, err)
	assert.Equal(t, int64(0), sum)
}

// TestReservationFinalizeRejectsCallerOverride proves the canonical finalizer
// rejects any caller attempt to override the derived vm_id/size_gb, so a stale or
// client-controlled finalization cannot leak capacity.
func TestReservationFinalizeRejectsCallerOverride(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid)

	res := &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 42}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, res))

	// Caller tries to sneak a different size.
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		_, ferr := resRepo.WithDB(tx).FinalizeTx(ctx, tx, res.ID, &models.VMDisk{Device: "vdb", Path: "/p", SizeGB: 999})
		return ferr
	}), ErrDiskReservationConflict)

	// Reservation remains pending (fail closed): no disk created.
	_, err := resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.NoError(t, err)
	var cnt int64
	require.NoError(t, db.Model(&models.VMDisk{}).Where("vm_id = ?", vmID).Count(&cnt).Error)
	assert.Equal(t, int64(0), cnt)
}

// TestReservationHardReleaseOnlySpecified proves ReleaseTx hard-deletes ONLY the
// specified pending reservation and does NOT bulk-release other pending reservations
// for the same user (a normal detach must not wipe sibling reservations). Each
// pending reservation lives on its own VM (one-pending-per-VM invariant).
func TestReservationHardReleaseOnlySpecified(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vm1 := seedDiskResVM(t, db, uid)
	vm2 := seedDiskResVM(t, db, uid)

	r1 := &models.DiskQuotaReservation{UserID: uid, VMID: vm1, SizeGB: 10}
	r2 := &models.DiskQuotaReservation{UserID: uid, VMID: vm2, SizeGB: 20}
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, r1))
	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, r2))

	// Release only r1.
	require.NoError(t, resRepo.WithDB(db).ReleaseTx(ctx, db, r1.ID))

	// r1 is gone; r2 still pending.
	_, err := resRepo.WithDB(db).GetPendingTx(ctx, db, r1.ID)
	require.ErrorIs(t, err, ErrDiskReservationNotFound)
	r2got, err := resRepo.WithDB(db).GetPendingTx(ctx, db, r2.ID)
	require.NoError(t, err)
	require.Equal(t, 20, r2got.SizeGB)

	// Releasing r1 again is a not-found (not a conflict): the row is gone.
	require.ErrorIs(t, resRepo.WithDB(db).ReleaseTx(ctx, db, r1.ID), ErrDiskReservationNotFound)
}

// TestReservationOnePendingPerVM proves a second pending reservation for the same
// VM is rejected with ErrDiskReservationConflict (one-pending-per-VM invariant).
func TestReservationOnePendingPerVM(t *testing.T) {
	ctx := context.Background()
	db, resRepo, _ := newDiskResTestDB(t)
	uid := seedDiskResUser(t, db, models.QuotaModeLegacy)
	vmID := seedDiskResVM(t, db, uid)

	require.NoError(t, resRepo.WithDB(db).CreateTx(ctx, db, &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 10}))
	err := resRepo.WithDB(db).CreateTx(ctx, db, &models.DiskQuotaReservation{UserID: uid, VMID: vmID, SizeGB: 5})
	require.ErrorIs(t, err, ErrDiskReservationConflict)
}

// Direct Upsert rejects a negative limit at the repository layer (defensive
// backstop; the service also rejects). Zero remains allowed (unlimited).
func TestQuotaRepoUpsertNegativeRejected(t *testing.T) {
	ctx := context.Background()
	db, repo := newQuotaRepoTestDB(t)
	uid := upsertSeedUser(t, db, models.QuotaModeLegacy)

	err := repo.Upsert(ctx, &models.UserQuota{UserID: uid, MaxVMs: -1})
	require.ErrorIs(t, err, ErrQuotaNegative)

	// Zero is still accepted (legacy unlimited semantics preserved).
	require.NoError(t, repo.Upsert(ctx, &models.UserQuota{UserID: uid, MaxVMs: 0, MaxDiskGB: 0}))
}
