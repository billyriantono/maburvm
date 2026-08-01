package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// sha256Hex returns the lowercase hex SHA-256 digest of seed. This mirrors the
// real 64-char token hashes stored by the model (no raw token value is ever
// held in the repo). It is a narrowly scoped test helper.
func sha256Hex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// EnrollmentTestSuite exercises the Phase 1A enrollment-control repository
// against an in-memory SQLite database. Schema is created explicitly (no
// AutoMigrate) with SQLite-compatible DDL mirroring migration 034.
//
// LIMITATION: SQLite is used only as a no-live-DB unit harness; it does NOT
// prove PostgreSQL triggers/constraints (the post-034 lane's NOT NULL FKs,
// CHECK constraints, or row-level behavior). These tests pin behavior at the
// repository/model layer; the DB-enforced invariants are validated separately.
type EnrollmentTestSuite struct {
	suite.Suite
	DB        *gorm.DB
	Repo      *EnrollmentRepository
	creatorID uuid.UUID

	// Seeded snapshot revisions referenced by invites (nonnullable FKs).
	quotaVersionID uuid.UUID
	urlRevisionID  uuid.UUID
	smtpRevisionID uuid.UUID
}

func (s *EnrollmentTestSuite) SetupTest() {
	db, err := gorm.Open(sqlite.Open("file:enroll?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	assert.NoError(s.T(), err)

	// Enable SQLite FK enforcement so the harness better approximates Postgres
	// invariants (SQLite has FK checks OFF by default).
	assert.NoError(s.T(), db.Exec("PRAGMA foreign_keys = ON").Error)

	ddl := []string{
		// Minimal users table: registration_invites/reset FK target.
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			token_revoked_at DATETIME, deleted_at DATETIME
		)`,
		// quota_policy_versions is owned by migration 033; declare a stub so the
		// FK reference in registration_invites resolves. FK enforcement is on.
		`CREATE TABLE IF NOT EXISTS quota_policy_versions (
			id TEXT PRIMARY KEY
		)`,
		`CREATE TABLE IF NOT EXISTS public_url_revisions (
			id TEXT PRIMARY KEY,
			origin TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'candidate',
			revision INTEGER NOT NULL UNIQUE,
			created_by TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS public_url_state (
			singleton_key TEXT PRIMARY KEY DEFAULT 'A',
			active_revision_id TEXT,
			state TEXT NOT NULL DEFAULT 'inactive',
			updated_by TEXT,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS smtp_config_revisions (
			id TEXT PRIMARY KEY,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			from_address TEXT NOT NULL,
			transport TEXT NOT NULL DEFAULT 'starttls',
			password_ciphertext BLOB NOT NULL,
			password_nonce BLOB NOT NULL,
			envelope_key_version INTEGER NOT NULL,
			state TEXT NOT NULL DEFAULT 'candidate',
			revision INTEGER NOT NULL UNIQUE,
			created_by TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS smtp_config_state (
			singleton_key TEXT PRIMARY KEY DEFAULT 'A',
			active_revision_id TEXT,
			state TEXT NOT NULL DEFAULT 'inactive',
			updated_by TEXT,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS registration_invites (
			id TEXT PRIMARY KEY,
			recipient_email TEXT NOT NULL,
			recipient_role TEXT NOT NULL DEFAULT 'client',
			creator_id TEXT NOT NULL,
			quota_policy_version_id TEXT NOT NULL,
			url_revision_id TEXT NOT NULL,
			smtp_revision_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			state TEXT NOT NULL DEFAULT 'pending_delivery',
			expires_at DATETIME NOT NULL,
			sent_at DATETIME,
			consumed_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (creator_id) REFERENCES users(id),
			FOREIGN KEY (quota_policy_version_id) REFERENCES quota_policy_versions(id),
			FOREIGN KEY (url_revision_id) REFERENCES public_url_revisions(id),
			FOREIGN KEY (smtp_revision_id) REFERENCES smtp_config_revisions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS password_reset_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			consumed_at DATETIME,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
	}
	for _, s2 := range ddl {
		assert.NoError(s.T(), db.Exec(s2).Error)
	}

	// Seed a creator user.
	s.creatorID = uuid.New()
	assert.NoError(s.T(), db.Exec(`INSERT INTO users (id, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		s.creatorID.String(), "creator@example.com", "hash", "admin").Error)

	// Seed the snapshot revisions referenced by invites (nonnullable FKs).
	s.quotaVersionID = uuid.New()
	assert.NoError(s.T(), db.Exec(`INSERT INTO quota_policy_versions (id) VALUES (?)`, s.quotaVersionID.String()).Error)

	s.urlRevisionID = uuid.New()
	assert.NoError(s.T(), db.Exec(`INSERT INTO public_url_revisions (id, origin, revision, state) VALUES (?, ?, ?, ?)`,
		s.urlRevisionID.String(), "https://panel.example.com", 100, string(models.PublicURLRevisionCandidate)).Error)

	s.smtpRevisionID = uuid.New()
	assert.NoError(s.T(), db.Exec(`INSERT INTO smtp_config_revisions (id, host, port, from_address, transport, password_ciphertext, password_nonce, envelope_key_version, revision, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.smtpRevisionID.String(), "smtp.example.com", 587, "noreply@example.com", string(models.SMTPTransportStartTLS),
		[]byte("cipher"), []byte("nonce"), 1, 100, string(models.SMTPRevisionCandidate)).Error)

	s.DB = db
	s.Repo = NewEnrollmentRepository(db)
}

func (s *EnrollmentTestSuite) TearDownTest() {
	s.DB.Exec("DELETE FROM registration_invites")
	s.DB.Exec("DELETE FROM password_reset_tokens")
	s.DB.Exec("DELETE FROM public_url_revisions")
	s.DB.Exec("DELETE FROM smtp_config_revisions")
	s.DB.Exec("DELETE FROM public_url_state")
	s.DB.Exec("DELETE FROM smtp_config_state")
	s.DB.Exec("DELETE FROM quota_policy_versions")
	s.DB.Exec("DELETE FROM users")
}

func TestEnrollmentTestSuite(t *testing.T) {
	suite.Run(t, new(EnrollmentTestSuite))
}

// realHash returns a deterministic 64-char lowercase hex string (SHA-256 shaped),
// never a raw token value. Used to mirror the Postgres VARCHAR(64) contract.
func realHash(seed string) string {
	// SHA-256 hex of the seed, lowercased to be safe.
	sum := sha256Hex(seed)
	return sum
}

func (s *EnrollmentTestSuite) TestCreateAndGetPublicURLRevision() {
	ctx := context.Background()
	rev := &models.PublicURLRevision{
		Origin:   "https://panel.example.com",
		Revision: 1,
		State:    models.PublicURLRevisionCandidate,
	}
	assert.NoError(s.T(), s.Repo.CreatePublicURLRevision(ctx, rev))
	assert.NotEqual(s.T(), uuid.Nil, rev.ID)

	got, err := s.Repo.GetPublicURLRevision(ctx, rev.ID)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), "https://panel.example.com", got.Origin)
	assert.Equal(s.T(), models.PublicURLRevisionCandidate, got.State)
}

func (s *EnrollmentTestSuite) TestCreateAndGetSMTPConfigRevision() {
	ctx := context.Background()
	rev := &models.SMTPConfigRevision{
		Host:               "smtp.example.com",
		Port:               587,
		FromAddress:        "noreply@example.com",
		Transport:          models.SMTPTransportStartTLS,
		PasswordCiphertext: []byte("cipher"),
		PasswordNonce:      []byte("nonce"),
		EnvelopeKeyVersion: 1,
		Revision:           1,
		State:              models.SMTPRevisionCandidate,
	}
	assert.NoError(s.T(), s.Repo.CreateSMTPConfigRevision(ctx, rev))
	assert.NotEqual(s.T(), uuid.Nil, rev.ID)

	got, err := s.Repo.GetSMTPConfigRevision(ctx, rev.ID)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), "smtp.example.com", got.Host)
	assert.Equal(s.T(), []byte("cipher"), got.PasswordCiphertext) // envelope only, never plaintext
}

func (s *EnrollmentTestSuite) TestActivePointerStateAbsentByDefault() {
	ctx := context.Background()
	rev, err := s.Repo.GetActivePublicURLRevision(ctx)
	assert.ErrorIs(s.T(), err, ErrNotConfigured)
	assert.Nil(s.T(), rev)

	st, err := s.Repo.GetPublicURLState(ctx)
	assert.ErrorIs(s.T(), err, ErrNotFound)
	assert.Nil(s.T(), st)
}

func (s *EnrollmentTestSuite) TestActiveURLRevCandidateNeverReturned() {
	ctx := context.Background()
	// Seed an 'active' state pointing at a candidate (non-active) revision.
	assert.NoError(s.T(), s.DB.Exec(
		`INSERT INTO public_url_state (singleton_key, active_revision_id, state) VALUES ('A', ?, 'active')`,
		s.urlRevisionID.String()).Error)

	rev, err := s.Repo.GetActivePublicURLRevision(ctx)
	assert.ErrorIs(s.T(), err, ErrConfigIntegrity)
	assert.Nil(s.T(), rev) // candidate/retired must never be surfaced as active
}

func (s *EnrollmentTestSuite) TestActiveURLRevMissingPointerIntegrity() {
	ctx := context.Background()
	// 'active' state but null pointer is contradictory.
	assert.NoError(s.T(), s.DB.Exec(
		`INSERT INTO public_url_state (singleton_key, active_revision_id, state) VALUES ('A', NULL, 'active')`).Error)

	rev, err := s.Repo.GetActivePublicURLRevision(ctx)
	assert.ErrorIs(s.T(), err, ErrConfigIntegrity)
	assert.Nil(s.T(), rev)
}

func (s *EnrollmentTestSuite) TestActiveURLRevOK() {
	ctx := context.Background()
	// Promote the seeded revision to active and point the singleton at it.
	assert.NoError(s.T(), s.DB.Exec(`UPDATE public_url_revisions SET state = ? WHERE id = ?`,
		string(models.PublicURLRevisionActive), s.urlRevisionID.String()).Error)
	assert.NoError(s.T(), s.DB.Exec(
		`INSERT INTO public_url_state (singleton_key, active_revision_id, state) VALUES ('A', ?, 'active')`,
		s.urlRevisionID.String()).Error)

	rev, err := s.Repo.GetActivePublicURLRevision(ctx)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), rev)
	assert.Equal(s.T(), s.urlRevisionID, rev.ID)
	assert.Equal(s.T(), models.PublicURLRevisionActive, rev.State)
}

func (s *EnrollmentTestSuite) TestActiveSMTPRevIntegrityAndOK() {
	ctx := context.Background()
	// Candidate-only revision must not be returned as active.
	assert.NoError(s.T(), s.DB.Exec(
		`INSERT INTO smtp_config_state (singleton_key, active_revision_id, state) VALUES ('A', ?, 'active')`,
		s.smtpRevisionID.String()).Error)
	rev, err := s.Repo.GetActiveSMTPConfigRevision(ctx)
	assert.ErrorIs(s.T(), err, ErrConfigIntegrity)
	assert.Nil(s.T(), rev)

	// Promote to active and re-check.
	assert.NoError(s.T(), s.DB.Exec(`UPDATE smtp_config_revisions SET state = ? WHERE id = ?`,
		string(models.SMTPRevisionActive), s.smtpRevisionID.String()).Error)
	rev, err = s.Repo.GetActiveSMTPConfigRevision(ctx)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), rev)
	assert.Equal(s.T(), s.smtpRevisionID, rev.ID)
}

func (s *EnrollmentTestSuite) TestCreatePendingInviteAndLookupByHash() {
	ctx := context.Background()
	inv := s.sampleInvite("pending@example.com", realHash("pending"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))

	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), "pending@example.com", got.RecipientEmail)
	assert.Equal(s.T(), models.RegistrationInvitePendingDelivery, got.State)
	assert.NotNil(s.T(), got.CreatorID)
	// Snapshot FKs are populated as nonnil values.
	assert.Equal(s.T(), s.quotaVersionID, got.QuotaPolicyVersionID)
	assert.Equal(s.T(), s.urlRevisionID, got.URLRevisionID)
	assert.Equal(s.T(), s.smtpRevisionID, got.SMTPRevisionID)
}

func (s *EnrollmentTestSuite) TestMarkInviteSentThenConsume() {
	ctx := context.Background()
	now := time.Now()
	inv := s.sampleInvite("consume@example.com", realHash("consume"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))

	// Cannot consume before sent (still pending_delivery).
	_, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now)
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)

	assert.NoError(s.T(), s.Repo.MarkInviteSent(ctx, inv.ID, now))

	// Now consumable.
	consumed, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now.Add(time.Minute))
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteConsumed, consumed.State)
	assert.NotNil(s.T(), consumed.ConsumedAt)

	// Second consume must fail (already consumed).
	_, err = s.Repo.ConsumeInvite(ctx, inv.TokenHash, now.Add(2*time.Minute))
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)
}

// TestConsumeExpiredInviteFails pins that an ACTIVE invite is only consumable
// within its 72h delivery window (sent_at + 72h). Under the synchronous-send
// contract the active expiry is always exactly 72h after sent_at (set by
// MarkInviteSent), so an invite delivered in the past becomes non-consumable
// only after that window elapses. We deliver now and attempt to consume well
// beyond 72h.
func (s *EnrollmentTestSuite) TestConsumeExpiredInviteFails() {
	ctx := context.Background()
	now := time.Now()
	inv := s.sampleInvite("expired@example.com", realHash("expired"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	// Deliver now: expiry becomes now + 72h.
	assert.NoError(s.T(), s.Repo.MarkInviteSent(ctx, inv.ID, now))

	// Consume strictly after the 72h window -> not consumable (expired).
	_, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now.Add(models.InviteActiveTTL).Add(time.Minute))
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)
}

// TestConsumeInviteTxOuterRollback verifies that ConsumeInviteTx participates
// in an outer transaction: an error after the consume rolls the whole tx back,
// leaving the token unconsumed. This pins the Phase 1B transaction contract.
func (s *EnrollmentTestSuite) TestConsumeInviteTxOuterRollback() {
	ctx := context.Background()
	now := time.Now()
	inv := s.sampleInvite("rollback@example.com", realHash("rollback"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	assert.NoError(s.T(), s.Repo.MarkInviteSent(ctx, inv.ID, now))

	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		repo := s.Repo.WithDB(tx)
		if _, err := repo.ConsumeInviteTx(tx, inv.TokenHash, now.Add(time.Minute)); err != nil {
			return err
		}
		// Intentional failure to force rollback of the consume above.
		return assert.AnError
	})
	assert.Error(s.T(), txErr)

	// After rollback the invite must remain active/unconsumed.
	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteActive, got.State)
	assert.Nil(s.T(), got.ConsumedAt)

	// And a subsequent, non-rolled-back consume succeeds.
	consumed, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now.Add(2*time.Minute))
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteConsumed, consumed.State)
}

// TestConsumeInviteTxSequentialConflict verifies that two sequential consumes
// within (or across) transactions conflict: the second conditional update
// reports RowsAffected == 0 and yields ErrInviteNotConsumable.
func (s *EnrollmentTestSuite) TestConsumeInviteTxSequentialConflict() {
	ctx := context.Background()
	now := time.Now()
	inv := s.sampleInvite("conflict@example.com", realHash("conflict"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	assert.NoError(s.T(), s.Repo.MarkInviteSent(ctx, inv.ID, now))

	// First consume inside its own tx.
	first, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now.Add(time.Minute))
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteConsumed, first.State)

	// Second consume (inside another tx) must detect the conflict via RowsAffected.
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := s.Repo.WithDB(tx).ConsumeInviteTx(tx, inv.TokenHash, now.Add(2*time.Minute))
		return err
	})
	assert.ErrorIs(s.T(), txErr, ErrInviteNotConsumable)
}

func (s *EnrollmentTestSuite) TestRevokeInvite() {
	ctx := context.Background()
	inv := s.sampleInvite("revoke@example.com", realHash("revoke"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	assert.NoError(s.T(), s.Repo.RevokeInvite(ctx, inv.ID))

	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteRevoked, got.State)

	// Cannot consume a revoked invite.
	_, err = s.Repo.ConsumeInvite(ctx, inv.TokenHash, time.Now())
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)

	// Revoking an already-revoked invite is a no-op conflict.
	err = s.Repo.RevokeInvite(ctx, inv.ID)
	assert.ErrorIs(s.T(), err, ErrInviteNotRevocable)
}

// TestMarkInviteDeliveryFailedTransition verifies the pending -> delivery_failed
// transition returns RowsAffected semantics and is not a retry queue.
func (s *EnrollmentTestSuite) TestMarkInviteDeliveryFailedTransition() {
	ctx := context.Background()
	inv := s.sampleInvite("failed@example.com", realHash("failed"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))

	// Transition pending -> delivery_failed succeeds.
	assert.NoError(s.T(), s.Repo.MarkInviteDeliveryFailed(ctx, inv.ID))

	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteDeliveryFailed, got.State)

	// A second attempt must report a conflict (no matching pending row).
	err = s.Repo.MarkInviteDeliveryFailed(ctx, inv.ID)
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)

	// A delivery_failed invite cannot be activated/sent.
	err = s.Repo.MarkInviteSent(ctx, inv.ID, time.Now())
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)
}

func (s *EnrollmentTestSuite) TestListPendingDeliveryAndByEmail() {
	ctx := context.Background()
	now := time.Now()
	a := s.sampleInvite("list@example.com", realHash("list-a"))
	b := s.sampleInvite("list@example.com", realHash("list-b"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, a))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, b))

	pending, err := s.Repo.ListPendingDeliveryInvites(ctx, now.Add(time.Hour))
	assert.NoError(s.T(), err)
	assert.Len(s.T(), pending, 2)

	byEmail, err := s.Repo.ListInvitesByEmail(ctx, "list@example.com")
	assert.NoError(s.T(), err)
	assert.Len(s.T(), byEmail, 2)
}

func (s *EnrollmentTestSuite) TestPasswordResetLifecycle() {
	ctx := context.Background()
	uid := s.creatorID
	tok := &models.PasswordResetToken{
		UserID:    uid,
		TokenHash: realHash("reset-1"),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	assert.NoError(s.T(), s.Repo.CreatePasswordResetToken(ctx, tok))
	assert.NotEqual(s.T(), uuid.Nil, tok.ID)

	got, err := s.Repo.GetResetTokenByHash(ctx, tok.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), uid, got.UserID)
	assert.Nil(s.T(), got.ConsumedAt)

	// Consume.
	consumed, err := s.Repo.ConsumeResetToken(ctx, tok.TokenHash, time.Now().Add(10*time.Minute))
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), consumed.ConsumedAt)
	assert.Equal(s.T(), 1, consumed.AttemptCount)

	// Second consume must fail (already consumed).
	_, err = s.Repo.ConsumeResetToken(ctx, tok.TokenHash, time.Now().Add(20*time.Minute))
	assert.ErrorIs(s.T(), err, ErrResetNotConsumable)
}

// TestExpiredPasswordResetExactExpiry verifies that at now == expires_at the
// token is expired (boundary inclusive) and therefore non-consumable.
func (s *EnrollmentTestSuite) TestExpiredPasswordResetExactExpiry() {
	ctx := context.Background()
	expiry := time.Now().UTC().Truncate(time.Second)
	tok := &models.PasswordResetToken{
		UserID:    s.creatorID,
		TokenHash: realHash("reset-exact"),
		ExpiresAt: expiry,
	}
	assert.NoError(s.T(), s.Repo.CreatePasswordResetToken(ctx, tok))

	// Exactly at expiry: non-consumable.
	_, err := s.Repo.ConsumeResetToken(ctx, tok.TokenHash, expiry)
	assert.ErrorIs(s.T(), err, ErrResetNotConsumable)

	// Attempt metadata recorded.
	got, err := s.Repo.GetResetTokenByHash(ctx, tok.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 1, got.AttemptCount)
	assert.Nil(s.T(), got.ConsumedAt)

	// Strictly after expiry: also non-consumable.
	_, err = s.Repo.ConsumeResetToken(ctx, tok.TokenHash, expiry.Add(time.Minute))
	assert.ErrorIs(s.T(), err, ErrResetNotConsumable)
}

func (s *EnrollmentTestSuite) TestConsumeResetTokenTxOuterRollback() {
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour)
	tok := &models.PasswordResetToken{
		UserID:    s.creatorID,
		TokenHash: realHash("reset-rollback"),
		ExpiresAt: expiry,
	}
	assert.NoError(s.T(), s.Repo.CreatePasswordResetToken(ctx, tok))

	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		repo := s.Repo.WithDB(tx)
		if _, err := repo.ConsumeResetTokenTx(tx, tok.TokenHash, time.Now().Add(10*time.Minute)); err != nil {
			return err
		}
		return assert.AnError
	})
	assert.Error(s.T(), txErr)

	// Rolled back: token remains unconsumed.
	got, err := s.Repo.GetResetTokenByHash(ctx, tok.TokenHash)
	assert.NoError(s.T(), err)
	assert.Nil(s.T(), got.ConsumedAt)

	// Subsequent non-rolled-back consume succeeds.
	consumed, err := s.Repo.ConsumeResetToken(ctx, tok.TokenHash, time.Now().Add(20*time.Minute))
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), consumed.ConsumedAt)
}

// TestConsumeInviteWrapperDomainError verifies the standalone ConsumeInvite
// wrapper returns the domain conflict ErrInviteNotConsumable (no mutation) and,
// crucially, does NOT mask it as a transaction success.
func (s *EnrollmentTestSuite) TestConsumeInviteWrapperDomainError() {
	ctx := context.Background()
	now := time.Now()
	inv := s.sampleInvite("wrap-invite@example.com", realHash("wrap-invite"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	// Not yet sent -> not consumable.
	_, err := s.Repo.ConsumeInvite(ctx, inv.TokenHash, now)
	assert.ErrorIs(s.T(), err, ErrInviteNotConsumable)

	// Missing token -> ErrNotFound, propagated (not swallowed as a tx success).
	_, err = s.Repo.ConsumeInvite(ctx, realHash("does-not-exist"), now)
	assert.ErrorIs(s.T(), err, ErrNotFound)
}

// TestConsumeResetWrapperCommitsAttempt verifies the standalone
// ConsumeResetToken wrapper commits the attempt_count/last_attempt_at update
// for a matched-but-non-consumable token while returning ErrResetNotConsumable.
func (s *EnrollmentTestSuite) TestConsumeResetWrapperCommitsAttempt() {
	ctx := context.Background()
	expiry := time.Now().UTC().Truncate(time.Second)
	tok := &models.PasswordResetToken{
		UserID:    s.creatorID,
		TokenHash: realHash("wrap-reset"),
		ExpiresAt: expiry,
	}
	assert.NoError(s.T(), s.Repo.CreatePasswordResetToken(ctx, tok))

	_, err := s.Repo.ConsumeResetToken(ctx, tok.TokenHash, expiry)
	assert.ErrorIs(s.T(), err, ErrResetNotConsumable)

	got, err := s.Repo.GetResetTokenByHash(ctx, tok.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 1, got.AttemptCount)
	assert.Nil(s.T(), got.ConsumedAt)
}

// TestConsumeWrappersPropagateTxError verifies the standalone Consume* wrappers
// propagate a genuine transaction (begin) failure rather than discarding it.
// This uses a repository bound to a *gorm.DB whose underlying pool is closed,
// so the explicit Transaction begin fails with a real DB error (no new deps).
func (s *EnrollmentTestSuite) TestConsumeWrappersPropagateTxError() {
	closedDB, err := gorm.Open(sqlite.Open("file:closed?mode=memory&cache=shared"), &gorm.Config{})
	assert.NoError(s.T(), err)
	sqlDB, err := closedDB.DB()
	assert.NoError(s.T(), err)
	assert.NoError(s.T(), sqlDB.Close())
	closedRepo := NewEnrollmentRepository(closedDB)

	_, err = closedRepo.ConsumeInvite(context.Background(), realHash("x"), time.Now())
	assert.Error(s.T(), err)
	assert.False(s.T(), errorsIsDomain(err))

	_, err = closedRepo.ConsumeResetToken(context.Background(), realHash("x"), time.Now())
	assert.Error(s.T(), err)
	assert.False(s.T(), errorsIsDomain(err))
}

// errorsIsDomain is a tiny local helper mirroring the repo's domain-error set
// so tests can assert a propagated error is genuinely a DB error, not a domain
// sentinel that the wrapper would have committed.
func errorsIsDomain(err error) bool {
	return errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInviteNotConsumable) ||
		errors.Is(err, ErrResetNotConsumable)
}

// sampleInvite builds an invite associated with the seeded creator user and the
// required nonnil snapshot revision IDs (FK contract).
func (s *EnrollmentTestSuite) sampleInvite(email, tokenHash string) *models.RegistrationInvite {
	return &models.RegistrationInvite{
		RecipientEmail:       email,
		RecipientRole:        "client",
		CreatorID:            s.creatorID,
		QuotaPolicyVersionID: s.quotaVersionID,
		URLRevisionID:        s.urlRevisionID,
		SMTPRevisionID:       s.smtpRevisionID,
		TokenHash:            tokenHash,
		State:                models.RegistrationInvitePendingDelivery,
		ExpiresAt:            time.Now().Add(24 * time.Hour),
	}
}

// TestTxPrimitivesRejectNilTx pins the Phase 1A transaction contract: every *Tx
// primitive must reject a nil *gorm.DB and must NOT silently fall back to the
// base DB (which would escape the caller's outer transaction). This is the
// Gate-1 remediation for transaction safety.
func (s *EnrollmentTestSuite) TestTxPrimitivesRejectNilTx() {
	ctx := context.Background()

	_, err := s.Repo.ConsumeInviteTx(nil, realHash("x"), time.Now())
	assert.ErrorIs(s.T(), err, ErrNilTransaction)

	_, err = s.Repo.ConsumeResetTokenTx(nil, realHash("x"), time.Now())
	assert.ErrorIs(s.T(), err, ErrNilTransaction)

	_, err = s.Repo.GetInviteForUpdate(nil, realHash("x"))
	assert.ErrorIs(s.T(), err, ErrNilTransaction)

	// And a nil tx must not have mutated anything via a silent fallback: the
	// base DB is untouched (sanity: repo still usable afterwards).
	inv := s.sampleInvite("nil-tx@example.com", realHash("nil-tx"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInvitePendingDelivery, got.State)
}

// TestRevokeExcludesDeliveryFailed pins that delivery_failed is a TERMINAL state
// and must NOT be revocable as if it were retryable (Gate-1 remediation). Only
// pending_delivery and active invites may be revoked.
func (s *EnrollmentTestSuite) TestRevokeExcludesDeliveryFailed() {
	ctx := context.Background()

	// pending -> delivery_failed is legal and terminal.
	inv := s.sampleInvite("revoke-failed@example.com", realHash("revoke-failed"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))
	assert.NoError(s.T(), s.Repo.MarkInviteDeliveryFailed(ctx, inv.ID))

	// Revoking a delivery_failed invite must be rejected (no-op conflict).
	err := s.Repo.RevokeInvite(ctx, inv.ID)
	assert.ErrorIs(s.T(), err, ErrInviteNotRevocable)

	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteDeliveryFailed, got.State)

	// Contrast: a still-pending invite CAN be revoked.
	pending := s.sampleInvite("revoke-pending@example.com", realHash("revoke-pending"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, pending))
	assert.NoError(s.T(), s.Repo.RevokeInvite(ctx, pending.ID))
	got, err = s.Repo.GetInviteByTokenHash(ctx, pending.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteRevoked, got.State)
}

// TestMarkInviteSentSets72hExpiry pins the synchronous-send contract: delivery
// makes the invite active AND sets expires_at to exactly sent_at + 72h (the
// model/repository behavior; the DB enforces the same via
// registration_invites_active_coherent). Before delivery the invite is pending
// with its original (immutable-at-creation) expiry; after delivery it expires
// exactly 72h after sent_at.
func (s *EnrollmentTestSuite) TestMarkInviteSentSets72hExpiry() {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	inv := s.sampleInvite("sent-72h@example.com", realHash("sent-72h"))
	assert.NoError(s.T(), s.Repo.CreatePendingInvite(ctx, inv))

	assert.NoError(s.T(), s.Repo.MarkInviteSent(ctx, inv.ID, now))

	got, err := s.Repo.GetInviteByTokenHash(ctx, inv.TokenHash)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), models.RegistrationInviteActive, got.State)
	assert.NotNil(s.T(), got.SentAt)
	assert.Equal(s.T(), now, got.SentAt.UTC().Truncate(time.Second))
	// Exactly 72h after sent_at.
	wantExpiry := now.Add(models.InviteActiveTTL)
	assert.Equal(s.T(), wantExpiry, got.ExpiresAt.UTC().Truncate(time.Second))
	// And it is consumable within the window.
	assert.True(s.T(), got.IsConsumable(now.Add(time.Hour)))
}

// TestConsumeResetTokenTxOuterTransaction pins that ConsumeResetTokenTx runs in
// the caller's outer transaction (it now rejects nil tx) and that attempt
// metadata is recorded under the outer tx. Used as a sibling to the SQLite
// outer-rollback tests for the invite path.
func (s *EnrollmentTestSuite) TestConsumeResetTokenTxOuterTransaction() {
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour)
	tok := &models.PasswordResetToken{
		UserID:    s.creatorID,
		TokenHash: realHash("reset-outer-tx"),
		ExpiresAt: expiry,
	}
	assert.NoError(s.T(), s.Repo.CreatePasswordResetToken(ctx, tok))

	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		repo := s.Repo.WithDB(tx)
		out, err := repo.ConsumeResetTokenTx(tx, tok.TokenHash, time.Now().Add(10*time.Minute))
		if err != nil {
			return err
		}
		// Surface the consumed record for post-commit assertion.
		_ = out
		return nil
	})
	assert.NoError(s.T(), txErr)

	got, err := s.Repo.GetResetTokenByHash(ctx, tok.TokenHash)
	assert.NoError(s.T(), err)
	assert.NotNil(s.T(), got.ConsumedAt)
	assert.Equal(s.T(), 1, got.AttemptCount)
}
