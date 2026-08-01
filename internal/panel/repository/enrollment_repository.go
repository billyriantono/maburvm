package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/maburvm/panel/internal/shared/models"
)

// ErrNilTransaction indicates a *Tx primitive was called with a nil *gorm.DB.
// The transaction-bearing primitives must NEVER silently fall back to the base
// connection; doing so would escape the caller's outer transaction (Phase 1B)
// and could commit outside it. Callers must pass an explicit tx (e.g. via
// WithDB(tx)) or use the standalone Consume* wrappers, which open their own.
var ErrNilTransaction = errors.New("enrollment repository: nil *gorm.DB passed to a transaction primitive")

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrInviteNotConsumable indicates a pending invite cannot be consumed
// (wrong state, expired, already consumed, or lost a conditional-update race).
var ErrInviteNotConsumable = errors.New("invite is not consumable")

// ErrInviteNotRevocable indicates an invite cannot be revoked (already
// consumed, already revoked, or no matching row).
var ErrInviteNotRevocable = errors.New("invite is not revocable")

// ErrResetNotConsumable indicates a reset token cannot be consumed
// (expired, already consumed, or lost a conditional-update race). A matched
// token that is merely non-consumable is still returned alongside this error.
var ErrResetNotConsumable = errors.New("reset token is not consumable")

// ErrConfigIntegrity indicates contradictory singleton/state rows: the active
// pointer disagrees with the referenced revision (pointer to a missing,
// candidate, or retired revision, or an 'active' state with no pointer). This
// is distinct from ErrNotConfigured and must never surface a candidate/retired
// revision as if it were active.
var ErrConfigIntegrity = errors.New("enrollment configuration integrity violation")

// ErrNotConfigured indicates no active configuration is present (missing
// singleton row, or an explicit 'inactive' state). This is the intentional
// "not configured" signal, distinct from ErrConfigIntegrity.
var ErrNotConfigured = errors.New("enrollment configuration not active")

// EnrollmentRepository provides data access for the Phase 1A enrollment-control
// plane. All primitives are transaction-friendly: they run on whichever
// *gorm.DB they are given (the base connection, or an explicit transaction
// injected via WithDB) and never return raw secrets or tokens. Token
// generation and mailer delivery are intentionally out of scope for this lane.
//
// IMPORTANT: A context is NOT a transaction carrier. Phase 1B owns a single
// outer transaction (invite lock + user create + finite quota snapshot +
// consume; reset similarly with password update + session revocation +
// consume) and passes it in via WithDB(tx) / the *Tx methods. The legacy
// standalone Consume* helpers remain only for callers without an outer tx and
// are implemented with an explicit r.db.Transaction.
type EnrollmentRepository struct {
	db *gorm.DB
}

// NewEnrollmentRepository creates a new EnrollmentRepository.
func NewEnrollmentRepository(db *gorm.DB) *EnrollmentRepository {
	return &EnrollmentRepository{db: db}
}

// WithDB returns an EnrollmentRepository bound to the supplied *gorm.DB
// (typically an explicit transaction opened by the caller). This is the
// established pattern (see VMRepository/IPAMRepository) for sharing a single
// outer transaction across repositories. The returned repository is stateless
// apart from the bound handle and is safe for short-lived use within a tx.
func (r *EnrollmentRepository) WithDB(db *gorm.DB) *EnrollmentRepository {
	return NewEnrollmentRepository(db)
}

// clauseLock provides a row-level lock for conditional consume transactions.
func clauseLock() clause.Locking {
	return clause.Locking{Strength: "UPDATE"}
}

// isDomainError reports whether err is an expected domain-level outcome that the
// standalone Consume* wrappers intentionally commit (as a no-op or a committed
// attempt update) rather than roll back. Genuine database errors (begin/callback/
// commit) are deliberately NOT included so that they force a rollback and are
// surfaced as the transaction error. Currently the domain results are:
//   - ErrNotFound: row absent, no mutation performed
//   - ErrInviteNotConsumable: invite not consumable, no mutation performed
//   - ErrResetNotConsumable: matched-but-non-consumable reset token, whose
//     attempt metadata is intentionally committed
func isDomainError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInviteNotConsumable) ||
		errors.Is(err, ErrResetNotConsumable)
}

// ===================== Public URL revisions =====================

// CreatePublicURLRevision inserts an immutable candidate revision.
func (r *EnrollmentRepository) CreatePublicURLRevision(ctx context.Context, rev *models.PublicURLRevision) error {
	return r.db.WithContext(ctx).Create(rev).Error
}

// GetPublicURLRevision returns a specific revision by ID.
func (r *EnrollmentRepository) GetPublicURLRevision(ctx context.Context, id uuid.UUID) (*models.PublicURLRevision, error) {
	var rev models.PublicURLRevision
	if err := r.db.WithContext(ctx).First(&rev, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rev, nil
}

// GetActivePublicURLRevision returns the revision pointed to by the singleton
// state row, but ONLY when the configuration is fully coherent:
//   - no singleton row          -> (nil, ErrNotConfigured)
//   - singleton state != active  -> (nil, ErrNotConfigured)
//   - active state, nil pointer  -> (nil, ErrConfigIntegrity)
//   - pointer to missing row     -> (nil, ErrConfigIntegrity)
//   - pointer to non-active rev  -> (nil, ErrConfigIntegrity)  (never candidate/retired)
//   - fully coherent active rev  -> (rev, nil)
func (r *EnrollmentRepository) GetActivePublicURLRevision(ctx context.Context) (*models.PublicURLRevision, error) {
	return getActivePublicURLRevisionDB(r.db.WithContext(ctx))
}

func getActivePublicURLRevisionDB(db *gorm.DB) (*models.PublicURLRevision, error) {
	var st models.PublicURLState
	if err := db.First(&st, "singleton_key = 'A'").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotConfigured
		}
		return nil, err
	}
	if st.State != "active" {
		// Explicit inactive (or any non-active) state is a valid "not
		// configured" signal, not an integrity violation.
		return nil, ErrNotConfigured
	}
	if st.ActiveRevisionID == nil {
		// Active state but no pointer is contradictory.
		return nil, ErrConfigIntegrity
	}
	var rev models.PublicURLRevision
	if err := db.First(&rev, "id = ?", *st.ActiveRevisionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Pointer references a missing revision.
			return nil, ErrConfigIntegrity
		}
		return nil, err
	}
	if rev.State != models.PublicURLRevisionActive {
		// Pointing at a candidate/retired revision is contradictory and must
		// never be surfaced as the active config.
		return nil, ErrConfigIntegrity
	}
	return &rev, nil
}

// GetPublicURLState returns the singleton state row (ErrNotFound if not yet created).
func (r *EnrollmentRepository) GetPublicURLState(ctx context.Context) (*models.PublicURLState, error) {
	var st models.PublicURLState
	if err := r.db.WithContext(ctx).First(&st, "singleton_key = 'A'").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &st, nil
}

// ===================== SMTP revisions =====================

// CreateSMTPConfigRevision inserts an immutable candidate SMTP revision. The
// caller supplies only the AES-GCM envelope fields; plaintext is never stored.
func (r *EnrollmentRepository) CreateSMTPConfigRevision(ctx context.Context, rev *models.SMTPConfigRevision) error {
	return r.db.WithContext(ctx).Create(rev).Error
}

// GetSMTPConfigRevision returns a specific revision by ID. Password envelope
// fields remain on the struct (internal use) but are json:"-".
func (r *EnrollmentRepository) GetSMTPConfigRevision(ctx context.Context, id uuid.UUID) (*models.SMTPConfigRevision, error) {
	var rev models.SMTPConfigRevision
	if err := r.db.WithContext(ctx).First(&rev, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rev, nil
}

// GetActiveSMTPConfigRevision returns the revision pointed to by the singleton
// state row, applying the same coherence/integrity contract as
// GetActivePublicURLRevision (see its docs). Returns ErrNotConfigured when
// absent/inactive and ErrConfigIntegrity when contradictory.
func (r *EnrollmentRepository) GetActiveSMTPConfigRevision(ctx context.Context) (*models.SMTPConfigRevision, error) {
	return getActiveSMTPConfigRevisionDB(r.db.WithContext(ctx))
}

func getActiveSMTPConfigRevisionDB(db *gorm.DB) (*models.SMTPConfigRevision, error) {
	var st models.SMTPConfigState
	if err := db.First(&st, "singleton_key = 'A'").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotConfigured
		}
		return nil, err
	}
	if st.State != "active" {
		return nil, ErrNotConfigured
	}
	if st.ActiveRevisionID == nil {
		return nil, ErrConfigIntegrity
	}
	var rev models.SMTPConfigRevision
	if err := db.First(&rev, "id = ?", *st.ActiveRevisionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrConfigIntegrity
		}
		return nil, err
	}
	if rev.State != models.SMTPRevisionActive {
		return nil, ErrConfigIntegrity
	}
	return &rev, nil
}

// GetSMTPConfigState returns the singleton state row (ErrNotFound if not yet created).
func (r *EnrollmentRepository) GetSMTPConfigState(ctx context.Context) (*models.SMTPConfigState, error) {
	var st models.SMTPConfigState
	if err := r.db.WithContext(ctx).First(&st, "singleton_key = 'A'").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &st, nil
}

// ===================== Registration invites (hash-only) =====================

// CreatePendingInvite inserts a pending-delivery invite using a precomputed
// SHA-256 token hash. The raw token is never provided or returned. Snapshot FK
// fields (quota/url/smtp revision IDs) are nonnullable on the model and must be
// populated to satisfy the post-034 invariants.
func (r *EnrollmentRepository) CreatePendingInvite(ctx context.Context, invite *models.RegistrationInvite) error {
	if invite.State == "" {
		invite.State = models.RegistrationInvitePendingDelivery
	}
	return r.db.WithContext(ctx).Create(invite).Error
}

// GetInviteByTokenHash returns the invite for a token hash, if any.
func (r *EnrollmentRepository) GetInviteByTokenHash(ctx context.Context, tokenHash string) (*models.RegistrationInvite, error) {
	var invite models.RegistrationInvite
	if err := r.db.WithContext(ctx).First(&invite, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &invite, nil
}

// GetInviteForUpdate loads an invite with a row-level lock (FOR UPDATE) on the
// supplied handle without creating a transaction. Phase 1B uses this inside its
// outer tx to read and validate the embedded snapshot before consuming.
func (r *EnrollmentRepository) GetInviteForUpdate(tx *gorm.DB, tokenHash string) (*models.RegistrationInvite, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	db := tx
	var invite models.RegistrationInvite
	if err := db.Clauses(clauseLock()).First(&invite, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &invite, nil
}

// ListInvitesByEmail returns invites for a recipient email ordered by newest.
func (r *EnrollmentRepository) ListInvitesByEmail(ctx context.Context, email string) ([]models.RegistrationInvite, error) {
	var invites []models.RegistrationInvite
	err := r.db.WithContext(ctx).Where("recipient_email = ?", email).Order("created_at DESC").Find(&invites).Error
	return invites, err
}

// ListPendingDeliveryInvites returns invites still awaiting delivery and not
// expired. NOTE: this is a read-only queue scan, NOT a retry queue. Hash-only
// pending invites are not a delivery retry backlog; see MarkInviteDeliveryFailed.
func (r *EnrollmentRepository) ListPendingDeliveryInvites(ctx context.Context, now time.Time) ([]models.RegistrationInvite, error) {
	var invites []models.RegistrationInvite
	err := r.db.WithContext(ctx).
		Where("state = ? AND expires_at > ?", models.RegistrationInvitePendingDelivery, now).
		Order("created_at ASC").
		Find(&invites).Error
	return invites, err
}

// MarkInviteSent transitions a pending invite to active once delivery is
// confirmed. Runs on the repository's DB without creating a transaction.
// Returns ErrInviteNotConsumable when no pending row matched (conflict).
func (r *EnrollmentRepository) MarkInviteSent(ctx context.Context, id uuid.UUID, now time.Time) error {
	return markInviteSentDB(r.db.WithContext(ctx), id, now)
}

func markInviteSentDB(db *gorm.DB, id uuid.UUID, now time.Time) error {
	// The synchronous-send contract: delivering an invite makes it active and it
	// MUST expire exactly 72 hours after sent_at. We set expires_at here so the
	// DB invariant (registration_invites_active_coherent) holds; the expires_at
	// column is immutable outside this pending->active transition.
	res := db.Model(&models.RegistrationInvite{}).
		Where("id = ? AND state = ?", id, models.RegistrationInvitePendingDelivery).
		Updates(map[string]interface{}{
			"state":      models.RegistrationInviteActive,
			"sent_at":    now,
			"expires_at": now.Add(models.InviteActiveTTL),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInviteNotConsumable
	}
	return nil
}

// MarkInviteDeliveryFailed transitions a pending invite to delivery_failed.
// Hash-only pending invites are NOT an implicit retry queue: this records a
// terminal delivery failure for the attempt. Runs on the repository's DB with
// no own transaction. Returns ErrInviteNotConsumable when no pending row
// matched (so the caller can detect a conflict / non-consumable transition).
func (r *EnrollmentRepository) MarkInviteDeliveryFailed(ctx context.Context, id uuid.UUID) error {
	return markInviteDeliveryFailedDB(r.db.WithContext(ctx), id)
}

func markInviteDeliveryFailedDB(db *gorm.DB, id uuid.UUID) error {
	res := db.Model(&models.RegistrationInvite{}).
		Where("id = ? AND state = ?", id, models.RegistrationInvitePendingDelivery).
		Update("state", models.RegistrationInviteDeliveryFailed)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInviteNotConsumable
	}
	return nil
}

// ConsumeInviteTx atomically marks an invite consumed if it is currently
// consumable (active, not consumed, not expired), running on the supplied tx
// handle WITHOUT opening its own transaction. This is the Phase 1B primitive:
// call it inside an outer tx (e.g. via WithDB(tx)). It locks the row, validates
// consumability, and performs a conditional update that checks RowsAffected to
// detect conflicts (lost race / already consumed). Returns ErrInviteNotConsumable
// when the conditional update matched no row.
func (r *EnrollmentRepository) ConsumeInviteTx(tx *gorm.DB, tokenHash string, now time.Time) (*models.RegistrationInvite, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	return consumeInviteDB(tx, tokenHash, now)
}

// ConsumeInvite is the LEGACY standalone wrapper. It opens its OWN explicit
// transaction via r.db.Transaction and is intended only for callers that do NOT
// already own an outer transaction. Phase 1B MUST use ConsumeInviteTx (via
// WithDB(tx)) so the consume participates in the single outer tx.
//
// Error precedence: a genuine database error (begin, callback, or commit) is
// propagated as the transaction error and takes precedence over any captured
// domain result; it also forces a rollback. Expected domain conflicts
// (ErrNotFound / ErrInviteNotConsumable) perform no mutation and are committed
// as a no-op, then surfaced to the caller.
func (r *EnrollmentRepository) ConsumeInvite(ctx context.Context, tokenHash string, now time.Time) (*models.RegistrationInvite, error) {
	var out *models.RegistrationInvite
	var innerErr error
	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out, innerErr = consumeInviteDB(tx, tokenHash, now)
		// Roll back and surface only genuine DB errors. Domain conflicts do not
		// mutate and are committed (no-op) so the caller still receives them.
		if innerErr != nil && !isDomainError(innerErr) {
			return innerErr
		}
		return nil
	})
	// begin/commit (or propagated genuine DB) errors take precedence.
	if txErr != nil {
		return nil, txErr
	}
	if innerErr != nil {
		return nil, innerErr
	}
	return out, nil
}

func consumeInviteDB(db *gorm.DB, tokenHash string, now time.Time) (*models.RegistrationInvite, error) {
	var invite models.RegistrationInvite
	if err := db.Clauses(clauseLock()).First(&invite, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !invite.IsConsumable(now) {
		return nil, ErrInviteNotConsumable
	}
	res := db.Model(&invite).
		Where("id = ? AND state = ? AND consumed_at IS NULL", invite.ID, models.RegistrationInviteActive).
		Updates(map[string]interface{}{
			"state":       models.RegistrationInviteConsumed,
			"consumed_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost a race or state changed under us: treat as not consumable.
		return nil, ErrInviteNotConsumable
	}
	invite.State = models.RegistrationInviteConsumed
	invite.ConsumedAt = &now
	return &invite, nil
}

// RevokeInvite marks an invite revoked if it is not yet consumed, running on
// the repository's DB without creating a transaction. Returns
// ErrInviteNotRevocable when no revocable row matched.
func (r *EnrollmentRepository) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	return revokeInviteDB(r.db.WithContext(ctx), id)
}

func revokeInviteDB(db *gorm.DB, id uuid.UUID) error {
	// delivery_failed is a TERMINAL state (a final failed-delivery record), NOT a
	// retryable/revocable one. Revocation is only valid for pending_delivery and
	// active invites. This matches the DB lifecycle guard (ec_invite_lifecycle).
	res := db.Model(&models.RegistrationInvite{}).
		Where("id = ? AND state IN ?", id, []string{
			string(models.RegistrationInvitePendingDelivery),
			string(models.RegistrationInviteActive),
		}).
		Update("state", models.RegistrationInviteRevoked)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInviteNotRevocable
	}
	return nil
}

// ===================== Password reset tokens (hash-only) =====================

// CreatePasswordResetToken inserts a hash-only reset token record. The raw
// token is never provided or returned.
func (r *EnrollmentRepository) CreatePasswordResetToken(ctx context.Context, tok *models.PasswordResetToken) error {
	return r.db.WithContext(ctx).Create(tok).Error
}

// GetResetTokenByHash returns the reset record for a token hash, if any.
func (r *EnrollmentRepository) GetResetTokenByHash(ctx context.Context, tokenHash string) (*models.PasswordResetToken, error) {
	var tok models.PasswordResetToken
	if err := r.db.WithContext(ctx).First(&tok, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &tok, nil
}

// ListResetTokensByUser returns reset records for a user ordered by newest.
func (r *EnrollmentRepository) ListResetTokensByUser(ctx context.Context, userID uuid.UUID) ([]models.PasswordResetToken, error) {
	var tokens []models.PasswordResetToken
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&tokens).Error
	return tokens, err
}

// ConsumeResetTokenTx marks a reset token consumed if currently consumable,
// running on the supplied tx handle WITHOUT opening its own transaction. This
// is the Phase 1B primitive: call it inside the outer tx (via WithDB(tx)).
// Attempt metadata (count + last attempt) is always recorded. On a matched but
// non-consumable token it returns (tok, ErrResetNotConsumable).
func (r *EnrollmentRepository) ConsumeResetTokenTx(tx *gorm.DB, tokenHash string, now time.Time) (*models.PasswordResetToken, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	return consumeResetTokenDB(tx, tokenHash, now)
}

// ConsumeResetToken is the LEGACY standalone wrapper. It opens its OWN explicit
// transaction via r.db.Transaction and is intended only for callers without an
// outer transaction. Phase 1B MUST use ConsumeResetTokenTx (via WithDB(tx)).
//
// Error precedence: a genuine database error (begin, callback, or commit) is
// propagated as the transaction error and takes precedence over any captured
// domain result; it also forces a rollback. The deliberate reset behavior is
// preserved: when a matched-but-non-consumable token increments attempt_count /
// last_attempt_at, that update commits while the caller receives
// ErrResetNotConsumable afterward. Other domain errors (ErrNotFound) perform no
// mutation and are committed as a no-op, then surfaced.
func (r *EnrollmentRepository) ConsumeResetToken(ctx context.Context, tokenHash string, now time.Time) (*models.PasswordResetToken, error) {
	var out *models.PasswordResetToken
	var innerErr error
	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out, innerErr = consumeResetTokenDB(tx, tokenHash, now)
		// Roll back and surface only genuine DB errors. The matched-but-
		// non-consumable attempt update (ErrResetNotConsumable) and other domain
		// conflicts perform their intended mutations / no-op and must commit.
		if innerErr != nil && !isDomainError(innerErr) {
			return innerErr
		}
		return nil
	})
	// begin/commit (or propagated genuine DB) errors take precedence.
	if txErr != nil {
		return nil, txErr
	}
	if innerErr != nil {
		return nil, innerErr
	}
	return out, nil
}

func consumeResetTokenDB(db *gorm.DB, tokenHash string, now time.Time) (*models.PasswordResetToken, error) {
	var tok models.PasswordResetToken
	if err := db.Clauses(clauseLock()).First(&tok, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	attempts := tok.AttemptCount + 1
	lastAttempt := now
	if !tok.IsConsumable(now) {
		// Expired or already consumed: record the attempt and signal
		// non-consumable. The token is returned for callers that need attempt
		// metadata, but (tok, ErrResetNotConsumable) is the contract.
		if err := db.Model(&tok).Where("id = ?", tok.ID).Updates(map[string]interface{}{
			"attempt_count":   attempts,
			"last_attempt_at": lastAttempt,
		}).Error; err != nil {
			return nil, err
		}
		tok.AttemptCount = attempts
		tok.LastAttemptAt = &lastAttempt
		return &tok, ErrResetNotConsumable
	}
	res := db.Model(&tok).
		Where("id = ? AND consumed_at IS NULL", tok.ID).
		Updates(map[string]interface{}{
			"consumed_at":     now,
			"attempt_count":   attempts,
			"last_attempt_at": lastAttempt,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost a race: a concurrent consumer already marked it consumed.
		return nil, ErrResetNotConsumable
	}
	tok.ConsumedAt = &now
	tok.AttemptCount = attempts
	tok.LastAttemptAt = &lastAttempt
	return &tok, nil
}
