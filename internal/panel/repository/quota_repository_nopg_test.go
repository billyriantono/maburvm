package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// quotaRepoUpsertSchema is a minimal SQLite mirror (migrations 033/037) sufficient
// to exercise Upsert's managed/direct boundary guard without a live PostgreSQL.
const quotaRepoUpsertSchema = `
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
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	quota_mode TEXT NOT NULL DEFAULT 'legacy',
	policy_id TEXT,
	policy_version INTEGER,
	policy_name TEXT,
	policy_assigned_at DATETIME,
	policy_assigned_by TEXT,
	cap_revision_id TEXT
);
`

func newQuotaRepoTestDB(t *testing.T) (*gorm.DB, *QuotaRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:quotarepupsert_"+uuid.NewString()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(quotaRepoUpsertSchema).Error)
	return db, NewQuotaRepository(db)
}

func upsertSeedUser(t *testing.T, db *gorm.DB, mode models.QuotaMode) string {
	t.Helper()
	u := &models.User{Email: "rep-" + uuid.NewString() + "@example.com", PasswordHash: "h", Role: models.RoleClient, QuotaMode: mode}
	require.NoError(t, db.Create(u).Error)
	return u.ID.String()
}

// Direct Upsert for a legacy user succeeds and persists the limits (legacy
// direct-write semantics preserved, including zero = unlimited).
func TestQuotaRepoUpsertLegacySucceeds(t *testing.T) {
	ctx := context.Background()
	db, repo := newQuotaRepoTestDB(t)
	uid := upsertSeedUser(t, db, models.QuotaModeLegacy)

	require.NoError(t, repo.Upsert(ctx, &models.UserQuota{UserID: uid, MaxVMs: 3, MaxVCPU: 4}))

	got, err := repo.GetByUserID(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 3, got.MaxVMs)
	require.Equal(t, 4, got.MaxVCPU)
}

// Direct Upsert for a managed user is rejected with the typed error (the row must
// be produced exclusively through AssignToUserQuotaTx).
func TestQuotaRepoUpsertManagedRejected(t *testing.T) {
	ctx := context.Background()
	db, repo := newQuotaRepoTestDB(t)
	uid := upsertSeedUser(t, db, models.QuotaModeManaged)

	err := repo.Upsert(ctx, &models.UserQuota{UserID: uid, MaxVMs: 5, MaxVCPU: 5})
	require.ErrorIs(t, err, ErrManagedQuotaDirectMutation)

	// No orphan row should be created for the rejected write.
	_, gerr := repo.GetByUserID(ctx, uid)
	require.ErrorIs(t, gerr, gorm.ErrRecordNotFound)
}

// Upsert targeting a missing user is rejected with the distinct ErrUserNotFound
// sentinel (not ErrManagedQuotaDirectMutation) so callers can map it to a 404; no
// orphan quota row is created via the legacy direct path.
func TestQuotaRepoUpsertMissingUserRejected(t *testing.T) {
	ctx := context.Background()
	_, repo := newQuotaRepoTestDB(t)
	err := repo.Upsert(ctx, &models.UserQuota{UserID: "does-not-exist", MaxVMs: 1})
	require.ErrorIs(t, err, ErrUserNotFound)
	require.NotErrorIs(t, err, ErrManagedQuotaDirectMutation)
}
