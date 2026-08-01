package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// quotaManagedSchema is a SQLite mirror of migrations 033 + 037 sufficient to
// exercise the fail-closed managed-quota service behavior without a live
// PostgreSQL.
const quotaManagedSchema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	role TEXT DEFAULT 'client',
	two_factor_secret TEXT,
	two_factor_backup_codes TEXT,
	ip_whitelist TEXT,
	quota_mode TEXT NOT NULL DEFAULT 'legacy',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	deleted_at DATETIME
);
CREATE TABLE IF NOT EXISTS user_quotas (
	user_id TEXT PRIMARY KEY,
	max_vms INTEGER NOT NULL DEFAULT 0,
	max_vcpu INTEGER NOT NULL DEFAULT 0,
	max_ram_mb INTEGER NOT NULL DEFAULT 0,
	max_disk_gb INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	quota_mode TEXT NOT NULL DEFAULT 'legacy',
	policy_id TEXT,
	policy_version INTEGER,
	policy_name TEXT,
	policy_assigned_at DATETIME,
	policy_assigned_by TEXT,
	cap_revision_id TEXT,
	FOREIGN KEY (policy_id, policy_version) REFERENCES quota_policy_versions (policy_id, version)
);
CREATE TABLE IF NOT EXISTS quota_policies (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	description TEXT,
	lifecycle TEXT NOT NULL DEFAULT 'active',
	is_default BOOLEAN NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS quota_policy_versions (
	id TEXT PRIMARY KEY,
	policy_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	max_vms INTEGER NOT NULL CHECK (max_vms > 0),
	max_vcpu INTEGER NOT NULL CHECK (max_vcpu > 0),
	max_ram_mb INTEGER NOT NULL CHECK (max_ram_mb > 0),
	max_disk_gb INTEGER NOT NULL CHECK (max_disk_gb > 0),
	cap_revision_id TEXT,
	note TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (policy_id, version)
);
CREATE TABLE IF NOT EXISTS platform_quota_cap_revisions (
	id TEXT PRIMARY KEY,
	max_vms INTEGER NOT NULL CHECK (max_vms > 0),
	max_vcpu INTEGER NOT NULL CHECK (max_vcpu > 0),
	max_ram_mb INTEGER NOT NULL CHECK (max_ram_mb > 0),
	max_disk_gb INTEGER NOT NULL CHECK (max_disk_gb > 0),
	state TEXT NOT NULL DEFAULT 'candidate',
	revision BIGINT NOT NULL UNIQUE,
	created_by TEXT,
	note TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	activated_at DATETIME,
	retired_at DATETIME
);
CREATE TABLE IF NOT EXISTS platform_quota_cap_state (
	singleton_key TEXT PRIMARY KEY DEFAULT 'A',
	active_revision_id TEXT,
	state TEXT NOT NULL DEFAULT 'inactive',
	updated_by TEXT,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS vms (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	hostname TEXT NOT NULL,
	os_template_id TEXT NOT NULL,
	resources TEXT,
	status TEXT DEFAULT 'stopped',
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

type QuotaManagedServiceTestSuite struct {
	suite.Suite
	DB            *gorm.DB
	svc           *QuotaService
	policyRepo    *repository.QuotaPolicyRepository
	quotaRepo     *repository.QuotaRepository
	legacyUserID  string
	managedUserID string
}

func (s *QuotaManagedServiceTestSuite) SetupSuite() {
	// Isolated suite DSN: a unique in-memory database per suite run so the
	// managed/legacy fixtures do not accidentally contaminate other test suites
	// that share a global SQLite connection. The schema is created fresh here.
	db, err := gorm.Open(sqlite.Open("file:quotamanaged_"+uuid.NewString()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(s.T(), err)
	for _, stmt := range []string{quotaManagedSchema} {
		require.NoError(s.T(), db.Exec(stmt).Error)
	}
	require.NoError(s.T(), db.Exec(`INSERT INTO platform_quota_cap_state (singleton_key, state) VALUES ('A','inactive')`).Error)

	s.DB = db
	s.policyRepo = repository.NewQuotaPolicyRepository(db)
	s.quotaRepo = repository.NewQuotaRepository(db)
	s.svc = NewQuotaService(db, repository.NewVMRepository(db))

	legacy := &models.User{Email: "svc-legacy@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: models.QuotaModeLegacy}
	require.NoError(s.T(), db.Create(legacy).Error)
	s.legacyUserID = legacy.ID.String()

	managed := &models.User{Email: "svc-managed@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: models.QuotaModeManaged}
	require.NoError(s.T(), db.Create(managed).Error)
	s.managedUserID = managed.ID.String()
}

func (s *QuotaManagedServiceTestSuite) TearDownSuite() {
	sqlDB, err := s.DB.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func (s *QuotaManagedServiceTestSuite) SetupTest() {
	s.DB.Exec("DELETE FROM user_quotas")
	s.DB.Exec("DELETE FROM quota_policy_versions")
	s.DB.Exec("DELETE FROM quota_policies")
	s.DB.Exec("DELETE FROM vms")
	s.DB.Exec("DELETE FROM platform_quota_cap_revisions")
	s.DB.Exec("UPDATE platform_quota_cap_state SET active_revision_id = NULL, state = 'inactive', updated_by = NULL")
}

func TestQuotaManagedServiceTestSuite(t *testing.T) {
	suite.Run(t, new(QuotaManagedServiceTestSuite))
}

// Legacy user with no quota row: unlimited (existing behavior preserved).
func (s *QuotaManagedServiceTestSuite) TestLegacyMissingQuotaIsUnlimited() {
	ctx := context.Background()
	q, err := s.svc.GetQuota(ctx, s.legacyUserID)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), q)
	assert.Equal(s.T(), 0, q.MaxVMs)
	assert.Equal(s.T(), 0, q.MaxVCPU)
}

// Legacy user with a zero row is unlimited and create/resize are allowed.
func (s *QuotaManagedServiceTestSuite) TestLegacyZeroQuotaAllowsAnything() {
	ctx := context.Background()
	require.NoError(s.T(), s.quotaRepo.Upsert(ctx, &models.UserQuota{
		UserID: s.legacyUserID, MaxVMs: 0, MaxVCPU: 0, MaxRAMMB: 0, MaxDiskGB: 0,
	}))
	assert.NoError(s.T(), s.svc.CheckCanCreate(ctx, s.legacyUserID, models.Resources{CPU: 9999, RAM: 1 << 30, Disk: 99999}))
}

// Managed user with no quota row fails closed (no silent unlimited).
func (s *QuotaManagedServiceTestSuite) TestManagedMissingQuotaFailsClosed() {
	ctx := context.Background()
	_, err := s.svc.GetQuota(ctx, s.managedUserID)
	assert.ErrorIs(s.T(), err, ErrQuotaNotAvailable)

	err = s.svc.CheckCanCreate(ctx, s.managedUserID, models.Resources{CPU: 1, RAM: 1, Disk: 1})
	assert.ErrorIs(s.T(), err, ErrQuotaNotAvailable)
}

// Managed user with an invalid snapshot (no provenance) fails closed.
func (s *QuotaManagedServiceTestSuite) TestManagedInvalidProvenanceFailsClosed() {
	ctx := context.Background()
	// A managed row with zero limits and no provenance must not be treated as usable.
	// Seed directly via DB (Upsert refuses managed users by design).
	require.NoError(s.T(), s.DB.Exec(
		`INSERT INTO user_quotas (user_id, max_vms, max_vcpu, max_ram_mb, max_disk_gb, quota_mode)
		 VALUES (?, 0, 0, 0, 0, 'managed')`, s.managedUserID).Error)
	_, err := s.svc.GetQuota(ctx, s.managedUserID)
	assert.ErrorIs(s.T(), err, ErrQuotaNotAvailable)
}

// Managed user with a valid, finite, provenance-complete snapshot resolves.
func (s *QuotaManagedServiceTestSuite) TestManagedValidSnapshotResolves() {
	ctx := context.Background()

	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, cap, "admin"))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, cap.ID, "admin"))

	p := &models.QuotaPolicy{Name: "SvcPolicy"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, v))
	require.NoError(s.T(), s.quotaRepo.AssignToUserQuota(ctx, s.managedUserID, v, "admin"))

	q, err := s.svc.GetQuota(ctx, s.managedUserID)
	require.NoError(s.T(), err)
	assert.True(s.T(), q.IsManaged())
	assert.Equal(s.T(), 5, q.MaxVMs)
}

// Direct SetQuota for a managed user is rejected with the typed error and must
// not write any row.
func (s *QuotaManagedServiceTestSuite) TestManagedSetQuotaRejected() {
	ctx := context.Background()
	_, err := s.svc.SetQuota(ctx, s.managedUserID, &SetQuotaRequest{MaxVMs: 10})
	assert.ErrorIs(s.T(), err, repository.ErrManagedQuotaDirectMutation)

	// No orphan/legacy row was created.
	_, gerr := s.quotaRepo.GetByUserID(ctx, s.managedUserID)
	assert.ErrorIs(s.T(), gerr, gorm.ErrRecordNotFound)
}

// Managed user + a stale legacy row (mode legacy in the row itself) still fails
// closed: the authoritative users.quota_mode governs, so a stray legacy row must
// not be used to resolve a managed user's quota.
func (s *QuotaManagedServiceTestSuite) TestManagedUserWithLegacyRowFailsClosed() {
	ctx := context.Background()
	// Upsert is rejected for managed users, so seed the row directly via DB to
	// simulate a pre-existing/stale row, with a legacy mode flag in the row.
	require.NoError(s.T(), s.DB.Exec(
		`INSERT INTO user_quotas (user_id, max_vms, max_vcpu, max_ram_mb, max_disk_gb, quota_mode)
		 VALUES (?, 5, 5, 5, 5, 'legacy')`, s.managedUserID).Error)

	// Even though the row is finite-positive, the authoritative user mode is
	// managed and the row lacks provenance, so it must fail closed.
	_, err := s.svc.GetQuota(ctx, s.managedUserID)
	assert.ErrorIs(s.T(), err, ErrQuotaNotAvailable)
}

// Managed user + a managed row missing required provenance fails closed.
func (s *QuotaManagedServiceTestSuite) TestManagedMalformedProvenanceFailsClosed() {
	ctx := context.Background()
	// A managed row that has finite-positive limits but is missing provenance
	// fields must not be treated as usable.
	require.NoError(s.T(), s.DB.Exec(
		`INSERT INTO user_quotas (user_id, max_vms, max_vcpu, max_ram_mb, max_disk_gb, quota_mode, policy_id)
		 VALUES (?, 5, 5, 5, 5, 'managed', 'some-policy-id')`, s.managedUserID).Error)

	_, err := s.svc.GetQuota(ctx, s.managedUserID)
	assert.ErrorIs(s.T(), err, ErrQuotaNotAvailable)
}

// Legacy user: missing row => unlimited (direct SetQuota unchanged semantics).
func (s *QuotaManagedServiceTestSuite) TestLegacyMissingSetQuotaUnchanged() {
	ctx := context.Background()
	q, err := s.svc.SetQuota(ctx, s.legacyUserID, &SetQuotaRequest{MaxVMs: 0, MaxVCPU: 0, MaxRAMMB: 0, MaxDiskGB: 0})
	require.NoError(s.T(), err)
	assert.Equal(s.T(), 0, q.MaxVMs)

	got, gerr := s.svc.GetQuota(ctx, s.legacyUserID)
	require.NoError(s.T(), gerr)
	assert.Equal(s.T(), 0, got.MaxVMs)
}

// Legacy user: a direct (non-zero) SetQuota write is preserved unchanged.
func (s *QuotaManagedServiceTestSuite) TestLegacyDirectSetQuotaPreserved() {
	ctx := context.Background()
	q, err := s.svc.SetQuota(ctx, s.legacyUserID, &SetQuotaRequest{MaxVMs: 7, MaxVCPU: 8, MaxRAMMB: 1024, MaxDiskGB: 50})
	require.NoError(s.T(), err)
	assert.Equal(s.T(), 7, q.MaxVMs)

	got, gerr := s.svc.GetQuota(ctx, s.legacyUserID)
	require.NoError(s.T(), gerr)
	assert.Equal(s.T(), 7, got.MaxVMs)
	assert.Equal(s.T(), 8, got.MaxVCPU)
}

// Authoritative mode lookup failure propagates (no fabricated unlimited row).
func (s *QuotaManagedServiceTestSuite) TestGetQuotaModeLookupFailurePropagates() {
	ctx := context.Background()
	// A non-existent user: GetUserQuotaMode returns ErrRecordNotFound, which must
	// propagate from GetQuota.
	_, err := s.svc.GetQuota(ctx, "nonexistent-user-id")
	assert.Error(s.T(), err)
	assert.NotErrorIs(s.T(), err, ErrQuotaNotAvailable)
}
