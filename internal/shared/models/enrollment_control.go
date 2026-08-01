package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ===================== Public URL control plane =====================

// PublicURLRevisionState is the lifecycle state of a canonical public URL
// revision. Revisions are append-only; content is immutable once written.
// Only the activation service (Phase 1B) transitions state.
type PublicURLRevisionState string

const (
	PublicURLRevisionCandidate PublicURLRevisionState = "candidate"
	PublicURLRevisionActive    PublicURLRevisionState = "active"
	PublicURLRevisionRetired   PublicURLRevisionState = "retired"
)

// PublicURLRevision is an immutable snapshot of the canonical public URL origin
// used in enrollment/invite emails. Only the normalized origin is stored.
type PublicURLRevision struct {
	ID          uuid.UUID              `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Origin      string                 `json:"origin" gorm:"type:varchar(512);not null" validate:"required"`
	Description string                 `json:"description" gorm:"type:varchar(512);not null;default:''"`
	State       PublicURLRevisionState `json:"state" gorm:"type:varchar(16);not null;default:'candidate'"`
	Revision    int64                  `json:"revision" gorm:"type:bigint;not null;uniqueIndex" validate:"required"`
	CreatedBy   *uuid.UUID             `json:"created_by,omitempty" gorm:"type:uuid"`
	CreatedAt   time.Time              `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time              `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for PublicURLRevision.
func (PublicURLRevision) TableName() string { return "public_url_revisions" }

// BeforeCreate generates a UUID when one is not supplied (mirrors the Postgres
// gen_random_uuid() default for environments without that function, e.g. tests).
func (r *PublicURLRevision) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// PublicURLState is the typed singleton active pointer/state for the public
// URL control plane. Exactly one row (singleton_key = 'A') exists.
type PublicURLState struct {
	SingletonKey     string     `json:"singleton_key" gorm:"type:varchar(1);primaryKey;default:'A'"`
	ActiveRevisionID *uuid.UUID `json:"active_revision_id,omitempty" gorm:"type:uuid;uniqueIndex"`
	State            string     `json:"state" gorm:"type:varchar(16);not null;default:'inactive'"`
	UpdatedBy        *uuid.UUID `json:"updated_by,omitempty" gorm:"type:uuid"`
	UpdatedAt        time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for PublicURLState.
func (PublicURLState) TableName() string { return "public_url_state" }

// ===================== SMTP control plane =====================

// SMTPTransport identifies the transport security used to submit mail.
type SMTPTransport string

const (
	SMTPTransportPlain    SMTPTransport = "plain"
	SMTPTransportStartTLS SMTPTransport = "starttls"
	SMTPTransportTLS      SMTPTransport = "tls"
)

// SMTPRevisionState is the lifecycle state of an SMTP configuration revision.
type SMTPRevisionState string

const (
	SMTPRevisionCandidate SMTPRevisionState = "candidate"
	SMTPRevisionActive    SMTPRevisionState = "active"
	SMTPRevisionRetired   SMTPRevisionState = "retired"
)

// SMTPConfigRevision is an immutable snapshot of SMTP (mailer) configuration.
// Non-secret connection fields are stored in clear; the password is stored
// only as an AES-GCM envelope. Encryption/mailer delivery is out of scope here.
type SMTPConfigRevision struct {
	ID                 uuid.UUID         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Host               string            `json:"host" gorm:"type:varchar(255);not null" validate:"required"`
	Port               int               `json:"port" gorm:"type:integer;not null" validate:"required"`
	Username           string            `json:"username" gorm:"type:varchar(255);not null;default:''"`
	FromAddress        string            `json:"from_address" gorm:"type:varchar(255);not null" validate:"required,email"`
	Transport          SMTPTransport     `json:"transport" gorm:"type:varchar(16);not null;default:'starttls'"`
	PasswordCiphertext []byte            `json:"-" gorm:"type:bytea;not null"`
	PasswordNonce      []byte            `json:"-" gorm:"type:bytea;not null"`
	EnvelopeKeyVersion int               `json:"-" gorm:"type:integer;not null"`
	State              SMTPRevisionState `json:"state" gorm:"type:varchar(16);not null;default:'candidate'"`
	Revision           int64             `json:"revision" gorm:"type:bigint;not null;uniqueIndex" validate:"required"`
	CreatedBy          *uuid.UUID        `json:"created_by,omitempty" gorm:"type:uuid"`
	CreatedAt          time.Time         `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt          time.Time         `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for SMTPConfigRevision.
func (SMTPConfigRevision) TableName() string { return "smtp_config_revisions" }

// BeforeCreate generates a UUID when one is not supplied.
func (r *SMTPConfigRevision) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// SMTPConfigState is the typed singleton active pointer/state for the SMTP
// control plane. Exactly one row (singleton_key = 'A') exists.
type SMTPConfigState struct {
	SingletonKey     string     `json:"singleton_key" gorm:"type:varchar(1);primaryKey;default:'A'"`
	ActiveRevisionID *uuid.UUID `json:"active_revision_id,omitempty" gorm:"type:uuid;uniqueIndex"`
	State            string     `json:"state" gorm:"type:varchar(16);not null;default:'inactive'"`
	UpdatedBy        *uuid.UUID `json:"updated_by,omitempty" gorm:"type:uuid"`
	UpdatedAt        time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for SMTPConfigState.
func (SMTPConfigState) TableName() string { return "smtp_config_state" }

// ===================== Registration invites (hash-only) =====================

// InviteActiveTTL is the active invite lifetime after successful delivery. Per
// the synchronous-send contract (migrations 034/036), an invite becomes active
// only upon successful delivery and MUST expire exactly 72 hours after sent_at.
// It is enforced both in Go (MarkInviteSent sets expires_at = sent_at + this)
// and at the DB layer (registration_invites_active_coherent CHECK).
const InviteActiveTTL = 72 * time.Hour

// RegistrationInviteState is the lifecycle state of a registration invite.
type RegistrationInviteState string

const (
	RegistrationInvitePendingDelivery RegistrationInviteState = "pending_delivery"
	RegistrationInviteActive          RegistrationInviteState = "active"
	RegistrationInviteDeliveryFailed  RegistrationInviteState = "delivery_failed"
	RegistrationInviteRevoked         RegistrationInviteState = "revoked"
	RegistrationInviteConsumed        RegistrationInviteState = "consumed"
)

// RegistrationInvite is a hash-only enrollment invite. Only the SHA-256 hex of
// the invite token is stored; no raw token column exists. Recipient role is
// client-only (enforced at DB level and validated here).
type RegistrationInvite struct {
	ID             uuid.UUID `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	RecipientEmail string    `json:"recipient_email" gorm:"type:varchar(255);not null" validate:"required,email"`
	RecipientRole  string    `json:"recipient_role" gorm:"type:varchar(16);not null;default:'client'"`
	CreatorID      uuid.UUID `json:"creator_id" gorm:"type:uuid;not null;index"`
	// Snapshot FK fields are nonnullable UUID values. The invite embeds an
	// immutable snapshot of the quota policy, public URL revision, and SMTP
	// revision at creation time; these must always reference existing rows to
	// satisfy the post-034 NOT NULL + FK invariants (do not rely on SQLite
	// silently omitting Postgres constraints).
	QuotaPolicyVersionID uuid.UUID               `json:"quota_policy_version_id" gorm:"type:uuid;not null;index"`
	URLRevisionID        uuid.UUID               `json:"url_revision_id" gorm:"type:uuid;not null;index"`
	SMTPRevisionID       uuid.UUID               `json:"smtp_revision_id" gorm:"type:uuid;not null;index"`
	TokenHash            string                  `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"` // SHA-256 hex
	State                RegistrationInviteState `json:"state" gorm:"type:varchar(20);not null;default:'pending_delivery'"`
	ExpiresAt            time.Time               `json:"expires_at" gorm:"not null"`
	SentAt               *time.Time              `json:"sent_at,omitempty" gorm:"index"`
	ConsumedAt           *time.Time              `json:"consumed_at,omitempty"`
	CreatedAt            time.Time               `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt            time.Time               `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for RegistrationInvite.
func (RegistrationInvite) TableName() string { return "registration_invites" }

// BeforeCreate generates a UUID when one is not supplied.
func (i *RegistrationInvite) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// IsConsumable reports whether the invite can currently be consumed.
func (i *RegistrationInvite) IsConsumable(now time.Time) bool {
	return i.State == RegistrationInviteActive &&
		i.ConsumedAt == nil &&
		now.Before(i.ExpiresAt)
}

// ===================== Password reset tokens (hash-only, additive) =====================

// PasswordResetToken is the canonical, hash-only password reset record. Only
// the SHA-256 hex of the reset token is stored; no raw token column exists.
// This replaces the legacy plaintext-token model semantics with a secure schema.
type PasswordResetToken struct {
	ID            uuid.UUID  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID        uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	TokenHash     string     `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"` // SHA-256 hex
	ExpiresAt     time.Time  `json:"expires_at" gorm:"not null;index"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
	AttemptCount  int        `json:"attempt_count" gorm:"not null;default:0"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt     time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for PasswordResetToken.
func (PasswordResetToken) TableName() string { return "password_reset_tokens" }

// BeforeCreate generates a UUID when one is not supplied.
func (t *PasswordResetToken) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// IsExpired reports whether the reset token has expired. Expiry is
// inclusive of the exact expiry instant: at now == expires_at the token is
// already expired and therefore non-consumable.
func (t *PasswordResetToken) IsExpired(now time.Time) bool {
	return now.Equal(t.ExpiresAt) || now.After(t.ExpiresAt)
}

// IsConsumable reports whether the reset token can currently be consumed.
func (t *PasswordResetToken) IsConsumable(now time.Time) bool {
	return t.ConsumedAt == nil && !t.IsExpired(now)
}
