package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// diskServiceSchema extends the SQLite mirror with vms, vm_disks, and the disk
// reservation table so the disk-admission lifecycle can be exercised without a
// live PostgreSQL.
const diskServiceSchema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '', email TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role TEXT DEFAULT 'client',
	two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0,
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

func newDiskServiceTestDB(t *testing.T) (*gorm.DB, *QuotaService) {
	t.Helper()
	// Unique DSN per invocation so isolated in-memory databases (no shared cache)
	// never cross-contaminate between tests, while still allowing the schema to be
	// created fresh each time.
	db, err := gorm.Open(sqlite.Open("file:disksvc_"+uuid.NewString()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	for _, stmt := range []string{diskServiceSchema} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	return db, NewQuotaService(db, repository.NewVMRepository(db))
}

func seedDSUser(t *testing.T, db *gorm.DB, mode models.QuotaMode) string {
	t.Helper()
	u := &models.User{Email: "ds-" + uuid.NewString() + "@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: mode}
	require.NoError(t, db.Create(u).Error)
	return u.ID.String()
}

func seedDSVM(t *testing.T, db *gorm.DB, userID string) string {
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

// Negative quota is rejected at the service layer (HTTP 400 later); zero remains
// the unlimited sentinel.
func TestServiceSetQuotaNegativeRejected(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)

	_, err := svc.SetQuota(ctx, uid, &SetQuotaRequest{MaxVMs: -1})
	require.ErrorIs(t, err, ErrQuotaNegative)

	// Zero legacy still allowed (unlimited).
	q, err := svc.SetQuota(ctx, uid, &SetQuotaRequest{MaxVMs: 0, MaxDiskGB: 0})
	require.NoError(t, err)
	assert.Equal(t, 0, q.MaxDiskGB)
}

// Legacy user with missing/zero quota: disk admission is unlimited (no rejection).
func TestServiceDiskAdmitLegacyUnlimited(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)
	vmID := seedDSVM(t, db, uid)

	res, err := svc.ReserveDiskQuota(ctx, uid, vmID, 1000)
	require.NoError(t, err)
	require.Equal(t, models.DiskQuotaReservationPending, res.Status)

	// Usage reflects the pending reservation over-count.
	used, err := svc.GetDiskUsage(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, 20+1000, used)

	// Release returns capacity.
	require.NoError(t, svc.ReleaseDiskReservation(ctx, res.ID))
	used2, err := svc.GetDiskUsage(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, 20, used2)
}

// Disk admission boundary: a positive limit is enforced, including pending
// reservations, and fails closed (ErrDiskQuotaExceeded) when exceeded.
func TestServiceDiskAdmitBoundaryEnforced(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)
	vmID := seedDSVM(t, db, uid) // boot disk 20GB

	// Set a 30GB disk limit: a 10GB disk fits (20+10=30).
	require.NoError(t, svc.repo.Upsert(ctx, &models.UserQuota{UserID: uid, MaxDiskGB: 30}))

	res, err := svc.ReserveDiskQuota(ctx, uid, vmID, 10)
	require.NoError(t, err)

	// Another 5GB would be 20+10(pending)+5 = 35 > 30 -> rejected.
	_, err = svc.ReserveDiskQuota(ctx, uid, vmID, 5)
	require.ErrorIs(t, err, ErrDiskQuotaExceeded)

	// Finalizing the first reservation through the CANONICAL safe primitive
	// (LockAndFinalizeReservationTx) converts it to real usage (still 30, fits).
	// It derives vm_id/size_gb from the locked reservation row and accepts only
	// non-quota agent output (device/path); no caller-provided VMID/SizeGB override.
	require.NoError(t, svc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, ferr := svc.LockAndFinalizeReservationTx(ctx, tx, res.ID, &models.VMDisk{
			Device: "vdb",
			Path:   "/p",
		})
		return ferr
	}))

	// Now 20 (boot) + 10 (extra) = 30; a further 1GB exceeds -> rejected.
	_, err = svc.ReserveDiskQuota(ctx, uid, vmID, 1)
	require.ErrorIs(t, err, ErrDiskQuotaExceeded)
}

// Managed user without a usable snapshot fails disk admission closed
// (ErrQuotaNotAvailable), not unlimited.
func TestServiceDiskAdmitManagedFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeManaged)
	vmID := seedDSVM(t, db, uid)

	_, err := svc.ReserveDiskQuota(ctx, uid, vmID, 10)
	require.ErrorIs(t, err, ErrQuotaNotAvailable)
}

// Finalization rejects a blank agent device/path (fail closed before a VMDisk
// with no backing volume is created). No reservation is consumed.
func TestServiceFinalizeRejectsBlankAgentDevicePath(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)
	vmID := seedDSVM(t, db, uid)

	res, err := svc.ReserveDiskQuota(ctx, uid, vmID, 10)
	require.NoError(t, err)

	for _, disk := range []*models.VMDisk{
		{Device: "", Path: "/p"},
		{Device: "vdb", Path: ""},
		{Device: "   ", Path: "  "},
	} {
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
			_, ferr := svc.LockAndFinalizeReservationTx(ctx, tx, res.ID, disk)
			return ferr
		}), repository.ErrDiskReservationConflict)
	}

	// The reservation is still pending and no VMDisk was created.
	_, gerr := svc.resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.NoError(t, gerr)
	var cnt int64
	require.NoError(t, db.Model(&models.VMDisk{}).Where("vm_id = ?", vmID).Count(&cnt).Error)
	assert.Equal(t, int64(0), cnt)
}

// The deprecated raw ConsumeDiskReservationTx surface is rejected (fail closed)
// so the unmodified attach path cannot perform an unsafe accounting swap. The
// pending reservation is retained.
func TestServiceRawConsumeRemainsRejected(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)
	vmID := seedDSVM(t, db, uid)

	res, err := svc.ReserveDiskQuota(ctx, uid, vmID, 10)
	require.NoError(t, err)

	require.ErrorIs(t, svc.ConsumeDiskReservationTx(ctx, db, res.ID), repository.ErrDiskReservationFinalizationRequired)

	// Reservation still pending (fail closed retained).
	_, gerr := svc.resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.NoError(t, gerr)
}

// A legacy user whose quota row carries a non-legacy (managed) mode is a
// marker/row mismatch that fails closed (no silent unlimited).
func TestServiceMarkerRowMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)

	// Seed a stray managed-mode row directly (Upsert would clear mode to legacy).
	require.NoError(t, db.Exec(
		`INSERT INTO user_quotas (user_id, max_vms, max_vcpu, max_ram_mb, max_disk_gb, quota_mode)
		 VALUES (?, 5, 5, 5, 5, 'managed')`, uid).Error)

	_, err := svc.GetQuota(ctx, uid)
	require.ErrorIs(t, err, ErrQuotaNotAvailable)
}

// Exact release revalidation: releasing a pending reservation requires the owner
// to hold the per-user admission lock and re-validates ownership; a consumed
// reservation cannot be released.
func TestServiceReleaseRevalidates(t *testing.T) {
	ctx := context.Background()
	db, svc := newDiskServiceTestDB(t)
	uid := seedDSUser(t, db, models.QuotaModeLegacy)
	vmID := seedDSVM(t, db, uid)

	res, err := svc.ReserveDiskQuota(ctx, uid, vmID, 10)
	require.NoError(t, err)

	// Release succeeds for the pending reservation and removes it.
	require.NoError(t, svc.ReleaseDiskReservation(ctx, res.ID))
	_, gerr := svc.resRepo.WithDB(db).GetPendingTx(ctx, db, res.ID)
	require.ErrorIs(t, gerr, repository.ErrDiskReservationNotFound)

	// A second release of the now-absent reservation is a not-found.
	require.ErrorIs(t, svc.ReleaseDiskReservation(ctx, res.ID), repository.ErrDiskReservationNotFound)
}
