-- Phase 1A enrollment-control data foundation (typed, versioned control plane).
-- Purely additive, no generic JSON secrets. Compatible with the Phase 1A
-- quota-policy foundation (migration 033) which owns the immutable
-- quota_policy_versions table; we only reference it via FK and never edit 033.
--
-- Lifecycle / invariants enforced here:
--   * public_url_revisions / smtp_config_revisions are append-only snapshots
--     (content is immutable; only lifecycle state transitions candidate ->
--     active -> retired, driven by the Phase 1B activation service).
--   * A typed singleton pointer/state row (public_url_state / smtp_config_state)
--     references the single active revision. Exactly one singleton row exists.
--   * registration_invites and password_reset_tokens store ONLY SHA-256 token
--     hashes (hex). No raw token column is ever created.
--   * SMTP password is stored ONLY as an AES-GCM envelope (ciphertext + nonce +
--     key version). No plaintext password column exists.
--   * No data is seeded and no active pointer is set by this migration.

-- ===================== Public URL control plane =====================

CREATE TABLE IF NOT EXISTS public_url_revisions (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    origin      VARCHAR(512) NOT NULL,                 -- normalized origin only (scheme://host[:port])
    description VARCHAR(512) NOT NULL DEFAULT '',
    state       VARCHAR(16)  NOT NULL DEFAULT 'candidate'
                    CHECK (state IN ('candidate', 'active', 'retired')),
    revision    BIGINT       NOT NULL UNIQUE,          -- monotonic, immutable snapshot number
    created_by  UUID,                                  -- operator who staged the revision
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_public_url_revisions_state ON public_url_revisions(state, revision DESC);

-- Typed singleton active pointer/state for the public URL control plane.
CREATE TABLE IF NOT EXISTS public_url_state (
    singleton_key       VARCHAR(1)   PRIMARY KEY DEFAULT 'A' CHECK (singleton_key = 'A'),
    active_revision_id  UUID         REFERENCES public_url_revisions(id),
    state               VARCHAR(16)  NOT NULL DEFAULT 'inactive'
                        CHECK (state IN ('inactive', 'active')),
    updated_by          UUID,
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ===================== SMTP control plane =====================

CREATE TABLE IF NOT EXISTS smtp_config_revisions (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    host                 VARCHAR(255) NOT NULL,
    port                 INTEGER      NOT NULL,
    username             VARCHAR(255) NOT NULL DEFAULT '',
    from_address         VARCHAR(255) NOT NULL,        -- normalized sender address
    transport            VARCHAR(16)  NOT NULL DEFAULT 'starttls'
                        CHECK (transport IN ('plain', 'starttls', 'tls')),
    -- AES-GCM envelope ONLY. Never store plaintext password.
    password_ciphertext  BYTEA        NOT NULL,
    password_nonce       BYTEA        NOT NULL,
    envelope_key_version INTEGER      NOT NULL,
    state                VARCHAR(16)  NOT NULL DEFAULT 'candidate'
                        CHECK (state IN ('candidate', 'active', 'retired')),
    revision             BIGINT       NOT NULL UNIQUE,
    created_by           UUID,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_smtp_config_revisions_state ON smtp_config_revisions(state, revision DESC);

-- Typed singleton active pointer/state for the SMTP control plane.
CREATE TABLE IF NOT EXISTS smtp_config_state (
    singleton_key       VARCHAR(1)   PRIMARY KEY DEFAULT 'A' CHECK (singleton_key = 'A'),
    active_revision_id  UUID         REFERENCES smtp_config_revisions(id),
    state               VARCHAR(16)  NOT NULL DEFAULT 'inactive'
                        CHECK (state IN ('inactive', 'active')),
    updated_by          UUID,
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ===================== Registration invites (hash-only) =====================

CREATE TABLE IF NOT EXISTS registration_invites (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_email        VARCHAR(255) NOT NULL,        -- normalized recipient email
    recipient_role         VARCHAR(16)  NOT NULL DEFAULT 'client'
                          CHECK (recipient_role = 'client'), -- client-only at DB level
    creator_id             UUID         NOT NULL REFERENCES users(id),
    quota_policy_version_id UUID        REFERENCES quota_policy_versions(id), -- FK to 033 immutable versions
    url_revision_id        UUID         REFERENCES public_url_revisions(id),
    smtp_revision_id       UUID         REFERENCES smtp_config_revisions(id),
    token_hash             VARCHAR(64)  NOT NULL UNIQUE, -- SHA-256 hex of the invite token
    state                  VARCHAR(20)  NOT NULL DEFAULT 'pending_delivery'
                          CHECK (state IN ('pending_delivery', 'active', 'delivery_failed', 'revoked', 'consumed')),
    expires_at             TIMESTAMPTZ  NOT NULL,
    sent_at                TIMESTAMPTZ,
    consumed_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Lookup-by-hash (consumption / delivery verification).
CREATE INDEX IF NOT EXISTS idx_registration_invites_token_hash ON registration_invites(token_hash);
-- Active delivery queue: pending invites not yet expired.
CREATE INDEX IF NOT EXISTS idx_registration_invites_delivery ON registration_invites(state, expires_at);
-- Email / creator access.
CREATE INDEX IF NOT EXISTS idx_registration_invites_email ON registration_invites(recipient_email, state);
CREATE INDEX IF NOT EXISTS idx_registration_invites_creator ON registration_invites(creator_id);

-- ===================== Password reset tokens (hash-only, additive) =====================
-- No prior SQL table exists despite a legacy Go model; this is the canonical,
-- hash-only schema. No raw token column is created.

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID        NOT NULL REFERENCES users(id),
    token_hash      VARCHAR(64) NOT NULL UNIQUE,        -- SHA-256 hex of the reset token
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    attempt_count   INTEGER     NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Lookup-by-hash (consumption / verification).
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_token_hash ON password_reset_tokens(token_hash);
-- Per-user active (unconsumed) token access.
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user ON password_reset_tokens(user_id, consumed_at);
-- Expiry sweeps.
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_expiry ON password_reset_tokens(expires_at);
