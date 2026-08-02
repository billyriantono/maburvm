package repository

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

	"github.com/maburvm/panel/internal/shared/models"
)

// quotaCapSchema mirrors migration 037 (minus the PG-only trigger; the app-layer
// checks enforce the same contract under SQLite) so the repository API contract
// can be exercised without a live PostgreSQL.
const quotaCapSchema = `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	email TEXT UNIQUE NOT NULL,
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
`

type QuotaCapRepositoryTestSuite struct {
	suite.Suite
	DB            *gorm.DB
	policyRepo    *QuotaPolicyRepository
	quotaRepo     *QuotaRepository
	adminID       string
	managedUserID string
	legacyUserID  string
}

func (s *QuotaCapRepositoryTestSuite) SetupSuite() {
	db, err := gorm.Open(sqlite.Open("file:quotacap?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(s.T(), err)
	for _, stmt := range []string{quotaCapSchema} {
		require.NoError(s.T(), db.Exec(stmt).Error)
	}
	// Seed the singleton cap-state row.
	require.NoError(s.T(), db.Exec(`INSERT INTO platform_quota_cap_state (singleton_key, state) VALUES ('A','inactive')`).Error)

	s.DB = db
	s.policyRepo = NewQuotaPolicyRepository(db)
	s.quotaRepo = NewQuotaRepository(db)

	admin := &models.User{Email: "cap-admin@example.com", PasswordHash: "h", Role: models.RoleAdmin}
	require.NoError(s.T(), db.Create(admin).Error)
	s.adminID = admin.ID.String()

	managed := &models.User{Email: "cap-managed@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: models.QuotaModeManaged}
	require.NoError(s.T(), db.Create(managed).Error)
	s.managedUserID = managed.ID.String()

	legacy := &models.User{Email: "cap-legacy@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: models.QuotaModeLegacy}
	require.NoError(s.T(), db.Create(legacy).Error)
	s.legacyUserID = legacy.ID.String()
}

func (s *QuotaCapRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := s.DB.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func (s *QuotaCapRepositoryTestSuite) SetupTest() {
	s.DB.Exec("DELETE FROM quota_policy_versions")
	s.DB.Exec("DELETE FROM quota_policies")
	s.DB.Exec("DELETE FROM platform_quota_cap_revisions")
	s.DB.Exec("UPDATE platform_quota_cap_state SET active_revision_id = NULL, state = 'inactive', updated_by = NULL")
	s.DB.Exec("DELETE FROM user_quotas")
}

func TestQuotaCapRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(QuotaCapRepositoryTestSuite))
}

// No active cap blocks publishing a policy version (fail closed).
func (s *QuotaCapRepositoryTestSuite) TestAppendVersionNoActiveCapFails() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "NeedCap"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))

	err := s.policyRepo.AppendVersion(ctx, p.ID, &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100})
	assert.ErrorIs(s.T(), err, ErrNoActiveQuotaCap)
}

// Creating and activating a cap, then publishing a version bound to it.
func (s *QuotaCapRepositoryTestSuite) TestCapActivateThenAppendVersionBindsCap() {
	ctx := context.Background()
	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, cap, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, cap.ID, s.adminID))

	active, err := s.policyRepo.GetActiveCapRevision(ctx)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), cap.ID, active.ID)

	p := &models.QuotaPolicy{Name: "WithCap"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, v))
	require.NotNil(s.T(), v.CapRevisionID)
	assert.Equal(s.T(), cap.ID, *v.CapRevisionID)
}

// Activating a cap lower than an active policy version is rejected.
func (s *QuotaCapRepositoryTestSuite) TestActivateCapLowerThanActivePolicyRejected() {
	ctx := context.Background()
	// Active cap wide enough.
	wide := &models.PlatformQuotaCapRevision{MaxVMs: 100, MaxVCPU: 200, MaxRAMMB: 524288, MaxDiskGB: 9000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, wide, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, wide.ID, s.adminID))

	p := &models.QuotaPolicy{Name: "ActivePolicy"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, &models.QuotaPolicyVersion{MaxVMs: 40, MaxVCPU: 80, MaxRAMMB: 200000, MaxDiskGB: 4000}))

	// Candidate cap lower than the active policy version must be rejected.
	low := &models.PlatformQuotaCapRevision{MaxVMs: 10, MaxVCPU: 10, MaxRAMMB: 10000, MaxDiskGB: 1000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, low, s.adminID))
	err := s.policyRepo.ActivateCapRevision(ctx, low.ID, s.adminID)
	assert.ErrorIs(s.T(), err, ErrQuotaCapLowerThanActivePolicy)
}

// Replacing the active cap retires the previous one and keeps a single active.
func (s *QuotaCapRepositoryTestSuite) TestCapReplacementRetiresPrevious() {
	ctx := context.Background()
	first := &models.PlatformQuotaCapRevision{MaxVMs: 20, MaxVCPU: 32, MaxRAMMB: 131072, MaxDiskGB: 2000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, first, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, first.ID, s.adminID))

	second := &models.PlatformQuotaCapRevision{MaxVMs: 60, MaxVCPU: 96, MaxRAMMB: 393216, MaxDiskGB: 6000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, second, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, second.ID, s.adminID))

	caps, err := s.policyRepo.ListCapRevisions(ctx)
	require.NoError(s.T(), err)
	activeCount := 0
	for _, c := range caps {
		if c.State == models.PlatformCapActive {
			activeCount++
			assert.Equal(s.T(), second.ID, c.ID)
		}
	}
	assert.Equal(s.T(), 1, activeCount)

	active, err := s.policyRepo.GetActiveCapRevision(ctx)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), second.ID, active.ID)
}

// AssignToUserQuotaTx requires a managed user; legacy user fails closed.
func (s *QuotaCapRepositoryTestSuite) TestAssignRejectsLegacyUser() {
	ctx := context.Background()
	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, cap, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, cap.ID, s.adminID))

	p := &models.QuotaPolicy{Name: "AssignCap"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, v))

	// Legacy user must not receive a managed snapshot.
	err := s.quotaRepo.AssignToUserQuotaTx(ctx, s.DB, s.legacyUserID, v, s.adminID)
	assert.ErrorIs(s.T(), err, ErrUserNotManaged)

	// nil transaction is rejected (no silent base-DB fallback).
	err = s.quotaRepo.AssignToUserQuotaTx(ctx, nil, s.managedUserID, v, s.adminID)
	assert.Error(s.T(), err)
}

// Successful managed assignment produces a full finite snapshot with provenance.
func (s *QuotaCapRepositoryTestSuite) TestAssignManagedFullProvenance() {
	ctx := context.Background()
	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, cap, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, cap.ID, s.adminID))

	p := &models.QuotaPolicy{Name: "FullProv"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, v))

	require.NoError(s.T(), s.quotaRepo.AssignToUserQuotaTx(ctx, s.DB, s.managedUserID, v, s.adminID))

	var q models.UserQuota
	require.NoError(s.T(), s.DB.First(&q, "user_id = ?", s.managedUserID).Error)
	assert.True(s.T(), q.IsManaged())
	assert.Equal(s.T(), 5, q.MaxVMs)
	require.NotNil(s.T(), q.PolicyID)
	assert.Equal(s.T(), p.ID, *q.PolicyID)
	require.NotNil(s.T(), q.PolicyVersion)
	assert.Equal(s.T(), 1, *q.PolicyVersion)
	require.NotNil(s.T(), q.CapRevisionID)
	assert.Equal(s.T(), cap.ID, *q.CapRevisionID)
	require.NotNil(s.T(), q.PolicyName)
	assert.Equal(s.T(), "FullProv", *q.PolicyName)
	require.NotNil(s.T(), q.PolicyAssignedBy)
	assert.Equal(s.T(), s.adminID, *q.PolicyAssignedBy)
}

// Outer-transaction rollback leaves no managed snapshot.
func (s *QuotaCapRepositoryTestSuite) TestAssignRollbackLeavesNoSnapshot() {
	ctx := context.Background()
	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.policyRepo.CreateCapRevision(ctx, cap, s.adminID))
	require.NoError(s.T(), s.policyRepo.ActivateCapRevision(ctx, cap.ID, s.adminID))

	p := &models.QuotaPolicy{Name: "RollbackProv"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.policyRepo.AppendVersion(ctx, p.ID, v))

	// Run the assignment inside a transaction that is rolled back.
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := s.quotaRepo.AssignToUserQuotaTx(ctx, tx, s.managedUserID, v, s.adminID); err != nil {
			return err
		}
		return assert.AnError // force rollback
	})
	require.Error(s.T(), txErr)

	var count int64
	require.NoError(s.T(), s.DB.Model(&models.UserQuota{}).Where("user_id = ?", s.managedUserID).Count(&count).Error)
	assert.Equal(s.T(), int64(0), count)
}

// No active cap blocks managed assignment (fail closed).
func (s *QuotaCapRepositoryTestSuite) TestAssignNoActiveCapFails() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "NoCapAssign"}
	require.NoError(s.T(), s.policyRepo.CreatePolicy(ctx, p))
	// Cannot append without an active cap, so craft a version directly that lacks a cap binding.
	v := &models.QuotaPolicyVersion{PolicyID: p.ID, Version: 1, MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.DB.Create(&v).Error)

	err := s.quotaRepo.AssignToUserQuotaTx(ctx, s.DB, s.managedUserID, v, s.adminID)
	assert.ErrorIs(s.T(), err, ErrNoActiveQuotaCap)
}

var _ = uuid.Nil
