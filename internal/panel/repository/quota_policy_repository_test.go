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

// quotaPolicySchema is a SQLite-compatible mirror of migration 033, used to
// exercise the repository API contract without a live PostgreSQL. CHECK and
// UNIQUE constraints are preserved so append-only/positive-limit behavior is
// verified at the store layer.
const quotaPolicySchema = `
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
	token_revoked_at DATETIME, deleted_at DATETIME
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
`

type QuotaPolicyRepositoryTestSuite struct {
	suite.Suite
	DB     *gorm.DB
	repo   *QuotaPolicyRepository
	userID string
}

func (s *QuotaPolicyRepositoryTestSuite) SetupSuite() {
	db, err := gorm.Open(sqlite.Open("file:quotapolicy?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(s.T(), err)
	for _, stmt := range []string{quotaPolicySchema} {
		require.NoError(s.T(), db.Exec(stmt).Error)
	}
	s.DB = db
	s.repo = NewQuotaPolicyRepository(db)

	// Seed the singleton cap-state row (mirrors migration 037).
	require.NoError(s.T(), db.Exec(`INSERT INTO platform_quota_cap_state (singleton_key, state) VALUES ('A','inactive')`).Error)

	user := &models.User{Email: "qp@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: models.QuotaModeManaged}
	require.NoError(s.T(), db.Create(user).Error)
	s.userID = user.ID.String()
}

func (s *QuotaPolicyRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := s.DB.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func (s *QuotaPolicyRepositoryTestSuite) SetupTest() {
	s.DB.Exec("DELETE FROM quota_policy_versions")
	s.DB.Exec("DELETE FROM quota_policies")
	s.DB.Exec("DELETE FROM user_quotas")
	s.DB.Exec("DELETE FROM platform_quota_cap_revisions")
	s.DB.Exec("UPDATE platform_quota_cap_state SET active_revision_id = NULL, state = 'inactive', updated_by = NULL")
	// Seed a wide active cap so version publishing/default-setting works in this
	// suite. The dedicated cap suite owns the "no active cap" negative cases.
	s.DB.Exec(`INSERT INTO platform_quota_cap_revisions (id, max_vms, max_vcpu, max_ram_mb, max_disk_gb, state, revision, activated_at)
		VALUES ('00000000-0000-0000-0000-0000000000aa', 9999, 9999, 99999999, 999999, 'active', 1, CURRENT_TIMESTAMP)`)
	s.DB.Exec(`UPDATE platform_quota_cap_state SET active_revision_id = '00000000-0000-0000-0000-0000000000aa', state = 'active'`)
}

func TestQuotaPolicyRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(QuotaPolicyRepositoryTestSuite))
}

func (s *QuotaPolicyRepositoryTestSuite) TestCreateAndGetPolicy() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Standard", Description: "default limits"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))
	assert.NotEmpty(s.T(), p.ID)

	got, err := s.repo.GetPolicy(ctx, p.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), "Standard", got.Name)
	assert.Equal(s.T(), models.QuotaPolicyActive, got.Lifecycle)
}

func (s *QuotaPolicyRepositoryTestSuite) TestCreatePolicyDuplicateName() {
	ctx := context.Background()
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, &models.QuotaPolicy{Name: "Dup"}))
	err := s.repo.CreatePolicy(ctx, &models.QuotaPolicy{Name: "Dup"})
	assert.ErrorIs(s.T(), err, ErrDuplicateQuotaPolicyName)
}

func (s *QuotaPolicyRepositoryTestSuite) TestAppendVersionIsMonotonicAndImmutable() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Mono"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))

	v1 := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v1))
	assert.Equal(s.T(), 1, v1.Version)

	v2 := &models.QuotaPolicyVersion{MaxVMs: 10, MaxVCPU: 8, MaxRAMMB: 16384, MaxDiskGB: 200}
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v2))
	assert.Equal(s.T(), 2, v2.Version)

	versions, err := s.repo.ListVersions(ctx, p.ID)
	require.NoError(s.T(), err)
	require.Len(s.T(), versions, 2)
	assert.Equal(s.T(), 1, versions[0].Version)
	assert.Equal(s.T(), 2, versions[1].Version)

	got, err := s.repo.GetVersion(ctx, p.ID, 1)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), 5, got.MaxVMs)
}

func (s *QuotaPolicyRepositoryTestSuite) TestAppendVersionRejectsNonPositiveLimits() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Bad"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))

	bad := &models.QuotaPolicyVersion{MaxVMs: 0, MaxVCPU: 1, MaxRAMMB: 1, MaxDiskGB: 1}
	err := s.repo.AppendVersion(ctx, p.ID, bad)
	// SQLite enforces the CHECK (max_vms > 0) constraint.
	assert.Error(s.T(), err)
}

func (s *QuotaPolicyRepositoryTestSuite) TestSingleActiveDefaultPolicy() {
	ctx := context.Background()
	a := &models.QuotaPolicy{Name: "A"}
	b := &models.QuotaPolicy{Name: "B"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, a))
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, b))

	// A default policy must have published at least one immutable version.
	require.NoError(s.T(), s.repo.AppendVersion(ctx, a.ID, &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}))
	require.NoError(s.T(), s.repo.AppendVersion(ctx, b.ID, &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}))

	require.NoError(s.T(), s.repo.SetDefaultPolicy(ctx, a.ID))
	def, err := s.repo.GetDefaultPolicy(ctx)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), a.ID, def.ID)

	// Switching the default must clear the previous one (no second default).
	require.NoError(s.T(), s.repo.SetDefaultPolicy(ctx, b.ID))
	def, err = s.repo.GetDefaultPolicy(ctx)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), b.ID, def.ID)

	others, err := s.repo.ListPolicies(ctx, models.QuotaPolicyActive)
	require.NoError(s.T(), err)
	countDefaults := 0
	for _, p := range others {
		if p.IsDefault {
			countDefaults++
		}
	}
	assert.Equal(s.T(), 1, countDefaults)
}

func (s *QuotaPolicyRepositoryTestSuite) TestDeprecatedPolicyCannotBeDefault() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Dep"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))
	require.NoError(s.T(), s.repo.SetPolicyLifecycle(ctx, p.ID, models.QuotaPolicyDeprecated))

	err := s.repo.SetDefaultPolicy(ctx, p.ID)
	assert.ErrorIs(s.T(), err, ErrMultipleDefaultQuotaPolicy)
}

// TestSetDefaultPolicyMissing rejects a non-existent policy id with a clean
// domain error rather than a DB failure.
func (s *QuotaPolicyRepositoryTestSuite) TestSetDefaultPolicyMissing() {
	err := s.repo.SetDefaultPolicy(context.Background(), uuid.New().String())
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotFound)
}

// TestSetDefaultPolicyNoVersion rejects an active policy that has published no
// immutable version; enrollment would otherwise have nothing to copy.
func (s *QuotaPolicyRepositoryTestSuite) TestSetDefaultPolicyNoVersion() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "NoVer"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))

	err := s.repo.SetDefaultPolicy(ctx, p.ID)
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyHasNoVersion)

	// Still not the default.
	_, err = s.repo.GetDefaultPolicy(ctx)
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotFound)
}

// TestSetDefaultPolicyValidAfterVersion confirms a policy becomes the default
// only once it has a published version.
func (s *QuotaPolicyRepositoryTestSuite) TestSetDefaultPolicyValidAfterVersion() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "ValidDefault"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))

	v := &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v))

	require.NoError(s.T(), s.repo.SetDefaultPolicy(ctx, p.ID))
	def, err := s.repo.GetDefaultPolicy(ctx)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), p.ID, def.ID)
}

// TestAppendVersionSequencingWithLock verifies monotonic, gap-free, 1-based
// version assignment under repeated appends. The parent row is held with a
// blocking FOR UPDATE lock inside the transaction so the sequence is serialized
// per policy (on PostgreSQL a concurrent appender waits for the lock rather than
// skipping it; SQLite is single-writer so the unit test exercises the logical
// sequencing only).
func (s *QuotaPolicyRepositoryTestSuite) TestAppendVersionSequencingWithLock() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Seq"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))

	const n = 5
	for i := 1; i <= n; i++ {
		v := &models.QuotaPolicyVersion{MaxVMs: i, MaxVCPU: i, MaxRAMMB: i * 1024, MaxDiskGB: i * 10}
		require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v))
		assert.Equal(s.T(), i, v.Version, "version should be assigned sequentially")
	}

	versions, err := s.repo.ListVersions(ctx, p.ID)
	require.NoError(s.T(), err)
	require.Len(s.T(), versions, n)
	for i, v := range versions {
		assert.Equal(s.T(), i+1, v.Version)
	}
}

// TestAppendVersionToDeprecatedRejected ensures a deprecated policy cannot grow
// new immutable versions; a publisher must create a fresh policy.
func (s *QuotaPolicyRepositoryTestSuite) TestAppendVersionToDeprecatedRejected() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "DepAppend"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))
	require.NoError(s.T(), s.repo.SetPolicyLifecycle(ctx, p.ID, models.QuotaPolicyDeprecated))

	v := &models.QuotaPolicyVersion{MaxVMs: 1, MaxVCPU: 1, MaxRAMMB: 1024, MaxDiskGB: 10}
	err := s.repo.AppendVersion(ctx, p.ID, v)
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotActive)
}

// TestAppendVersionMissingPolicy surfaces a clean not-found error.
func (s *QuotaPolicyRepositoryTestSuite) TestAppendVersionMissingPolicy() {
	v := &models.QuotaPolicyVersion{MaxVMs: 1, MaxVCPU: 1, MaxRAMMB: 1024, MaxDiskGB: 10}
	err := s.repo.AppendVersion(context.Background(), uuid.New().String(), v)
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotFound)
}

func (s *QuotaPolicyRepositoryTestSuite) TestClearDefaultPolicy() {
	ctx := context.Background()
	p := &models.QuotaPolicy{Name: "Clr"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))
	// A default must have a published version.
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, &models.QuotaPolicyVersion{MaxVMs: 5, MaxVCPU: 4, MaxRAMMB: 8192, MaxDiskGB: 100}))
	require.NoError(s.T(), s.repo.SetDefaultPolicy(ctx, p.ID))

	require.NoError(s.T(), s.repo.ClearDefaultPolicy(ctx))
	_, err := s.repo.GetDefaultPolicy(ctx)
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotFound)
}

func (s *QuotaPolicyRepositoryTestSuite) TestAssignToUserQuotaSnapshotNoLiveDependency() {
	ctx := context.Background()

	// An active cap is required to publish a version (fail-closed otherwise).
	cap := &models.PlatformQuotaCapRevision{MaxVMs: 50, MaxVCPU: 64, MaxRAMMB: 262144, MaxDiskGB: 5000}
	require.NoError(s.T(), s.repo.CreateCapRevision(ctx, cap, uuid.New().String()))
	require.NoError(s.T(), s.repo.ActivateCapRevision(ctx, cap.ID, uuid.New().String()))

	p := &models.QuotaPolicy{Name: "Assign"}
	require.NoError(s.T(), s.repo.CreatePolicy(ctx, p))
	v := &models.QuotaPolicyVersion{MaxVMs: 3, MaxVCPU: 2, MaxRAMMB: 4096, MaxDiskGB: 50}
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v))

	require.NoError(s.T(), s.repo.AssignToUserQuota(ctx, s.userID, v, uuid.New().String()))

	var q models.UserQuota
	require.NoError(s.T(), s.DB.First(&q, "user_id = ?", s.userID).Error)
	assert.True(s.T(), q.IsManaged())
	assert.Equal(s.T(), 3, q.MaxVMs)
	assert.True(s.T(), q.HasPolicyProvenance())
	require.NotNil(s.T(), q.PolicyID)
	assert.Equal(s.T(), p.ID, *q.PolicyID)
	assert.Equal(s.T(), 1, *q.PolicyVersion)
	require.NotNil(s.T(), q.CapRevisionID)
	assert.Equal(s.T(), cap.ID, *q.CapRevisionID)

	// Re-assigning a later version overwrites the snapshot (no live FKs).
	v2 := &models.QuotaPolicyVersion{MaxVMs: 9, MaxVCPU: 8, MaxRAMMB: 16384, MaxDiskGB: 200}
	require.NoError(s.T(), s.repo.AppendVersion(ctx, p.ID, v2))
	require.NoError(s.T(), s.repo.AssignToUserQuota(ctx, s.userID, v2, uuid.New().String()))
	require.NoError(s.T(), s.DB.First(&q, "user_id = ?", s.userID).Error)
	assert.Equal(s.T(), 9, q.MaxVMs)
	assert.Equal(s.T(), 2, *q.PolicyVersion)
}

func (s *QuotaPolicyRepositoryTestSuite) TestGetPolicyNotFound() {
	_, err := s.repo.GetPolicy(context.Background(), uuid.New().String())
	assert.ErrorIs(s.T(), err, ErrQuotaPolicyNotFound)
}
