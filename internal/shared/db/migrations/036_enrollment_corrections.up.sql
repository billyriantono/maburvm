-- 036_enrollment_corrections: post-035 Gate-1 remediation for the enrollment
-- control plane. Forward-only, additive, fail-closed.
--
-- Precondition: 033, 033a (shape guard, no-op if already canonical), 033b, 034,
-- 035 are recorded. 036 corrects three classes of defect found after 035:
--
--   1. SMTP immutability trigger bug: 035's ec_revisions_insert_guard referenced
--      a nonexistent `NEW.description` column on smtp_config_revisions (which has
--      no description column). That made ANY UPDATE on an SMTP revision fail. We
--      recreate the guard so the SMTP branch covers only real payload fields.
--      Immutability now covers ID, created_at, and every actual payload field.
--   2. Singleton/active invariants were incomplete: 035 guaranteed
--      (pointer NULL <=> state inactive) and (pointer references an active
--      revision) but did NOT guarantee "inactive iff ZERO active revisions" nor
--      "active iff EXACTLY ONE active revision AND pointer equals it". This left
--      an orphan-active-state hole (state active with the pointed revision
--      retired but a different active revision lingering). 036 strengthens the
--      deferred consistency check to close both holes and forbid orphan pointers
--      / orphan active states at COMMIT. No delete, no implicit active config.
--   3. Reset + invite DB lifecycle backstops were weak or missing:
--        * reset: consumed_at must be NULL -> timestamp and immutable after;
--          attempt_count must stay non-negative and monotonic;
--        * invite: sent_at / consumed_at immutable once set; an active invite
--          requires sent_at and MUST expire exactly 72h after sent_at (the
--          synchronous-send contract selected in 034); pending has no delivery
--          activation semantics (sent_at/consumed_at must stay NULL in pending).
--
-- 036 also POSTCONDITION-CHECKS the applied DB: if any raw bearer-token column
-- (`token`) remains in password_reset_tokens, it fails closed with an actionable
-- remediation message. Raw tokens are never migrated or reinterpreted.
--
-- Everything here is CREATE OR REPLACE / ADD CONSTRAINT IF NOT EXISTS so it is
-- idempotent and safe to re-run. It never edits 033-035 source; it only
-- re-defines guarding functions and adds backstop constraints.

-- ===================== B. CORRECTIONS: control-plane guards =====================

-- B2 (corrected). Revisions insert as candidate; payload/ID/created_at immutable.
-- FIX: drop the bogus `NEW.description` reference from the SMTP branch (that
-- table has no description column) and add id + created_at to the immutable set
-- so a revision's identity and origin timestamp cannot be rewritten. updated_at
-- is intentionally left mutable (audit metadata, not a payload field).
CREATE OR REPLACE FUNCTION ec_revisions_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF TG_TABLE_NAME = 'public_url_revisions' THEN
            NEW.state := 'candidate';
        ELSIF TG_TABLE_NAME = 'smtp_config_revisions' THEN
            NEW.state := 'candidate';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF NEW.id IS DISTINCT FROM OLD.id THEN
            RAISE EXCEPTION 'revision id is immutable';
        END IF;
        IF NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'revision created_at is immutable';
        END IF;
        IF NEW.revision IS DISTINCT FROM OLD.revision THEN
            RAISE EXCEPTION 'revision is immutable';
        END IF;
        IF TG_TABLE_NAME = 'public_url_revisions' THEN
            IF NEW.origin IS DISTINCT FROM OLD.origin OR
               NEW.description IS DISTINCT FROM OLD.description OR
               NEW.created_by IS DISTINCT FROM OLD.created_by THEN
                RAISE EXCEPTION 'revision payload is immutable';
            END IF;
        ELSIF TG_TABLE_NAME = 'smtp_config_revisions' THEN
            IF NEW.host IS DISTINCT FROM OLD.host OR
               NEW.port IS DISTINCT FROM OLD.port OR
               NEW.username IS DISTINCT FROM OLD.username OR
               NEW.from_address IS DISTINCT FROM OLD.from_address OR
               NEW.transport IS DISTINCT FROM OLD.transport OR
               NEW.created_by IS DISTINCT FROM OLD.created_by OR
               NEW.password_ciphertext IS DISTINCT FROM OLD.password_ciphertext OR
               NEW.password_nonce IS DISTINCT FROM OLD.password_nonce OR
               NEW.envelope_key_version IS DISTINCT FROM OLD.envelope_key_version THEN
                RAISE EXCEPTION 'revision payload is immutable';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_public_url_revisions_insert_guard ON public_url_revisions;
CREATE TRIGGER ec_public_url_revisions_insert_guard
    BEFORE INSERT OR UPDATE ON public_url_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_insert_guard();

DROP TRIGGER IF EXISTS ec_smtp_config_revisions_insert_guard ON smtp_config_revisions;
CREATE TRIGGER ec_smtp_config_revisions_insert_guard
    BEFORE INSERT OR UPDATE ON smtp_config_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_insert_guard();

-- B7 (corrected). Singleton/state reconciliation deferred to COMMIT, now closing
-- the inactive/active holes:
--   * state 'inactive'  <=> pointer IS NULL AND zero active revisions exist.
--   * state 'active'    <=> exactly ONE active revision exists AND the pointer
--                           equals that revision's id.
-- This forbids orphan-active-state (state active while the pointed revision was
-- retired and another active lingers) and orphan pointers. Both revisions and
-- the state row are checked; the check reads the CURRENT (uncommitted) row state
-- so a retirement+activation txn that ends with exactly one active revision and a
-- matching pointer passes. No active singleton is ever created implicitly.
CREATE OR REPLACE FUNCTION ec_control_state_consistent()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_rev_table text := TG_ARGV[0];
    v_state_table text := TG_ARGV[1];
    v_pointer uuid;
    v_state text;
    v_rev_state text;
    v_active_count int;
BEGIN
    EXECUTE format(
        'SELECT active_revision_id, state FROM %I WHERE singleton_key = ''A''',
        v_state_table
    ) INTO v_pointer, v_state;

    EXECUTE format(
        'SELECT count(*) FROM %I WHERE state = ''active''', v_rev_table
    ) INTO v_active_count;

    IF v_state = 'inactive' THEN
        IF v_pointer IS NOT NULL THEN
            RAISE EXCEPTION 'control-plane state inactive requires a null pointer';
        END IF;
        IF v_active_count <> 0 THEN
            RAISE EXCEPTION
                'control-plane state inactive requires ZERO active revisions, '
                'but % active revision(s) exist. Retire all active revisions '
                '(or clear the pointer) before entering the inactive state.',
                v_active_count;
        END IF;
        RETURN NULL;
    END IF;

    IF v_state = 'active' THEN
        IF v_pointer IS NULL THEN
            RAISE EXCEPTION 'control-plane state active requires a non-null pointer';
        END IF;
        IF v_active_count <> 1 THEN
            RAISE EXCEPTION
                'control-plane state active requires EXACTLY ONE active revision, '
                'but % active revision(s) exist.', v_active_count;
        END IF;
        EXECUTE format(
            'SELECT state FROM %I WHERE id = $1', v_rev_table
        ) USING v_pointer INTO v_rev_state;
        IF v_rev_state IS DISTINCT FROM 'active' THEN
            RAISE EXCEPTION 'active pointer must reference the single active revision';
        END IF;
        -- Pointer must equal the single active revision (no orphan pointer).
        EXECUTE format(
            'SELECT id FROM %I WHERE state = ''active''', v_rev_table
        ) INTO v_rev_state;  -- reuse variable to hold the active id
        IF v_pointer::text <> v_rev_state::text THEN
            RAISE EXCEPTION 'active pointer must equal the single active revision id';
        END IF;
    END IF;
    RETURN NULL;
END $$;

DROP TRIGGER IF EXISTS ec_public_url_state_deferred ON public_url_revisions;
CREATE CONSTRAINT TRIGGER ec_public_url_state_deferred
    AFTER INSERT OR UPDATE OR DELETE ON public_url_revisions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('public_url_revisions', 'public_url_state');

DROP TRIGGER IF EXISTS ec_public_url_state_deferred2 ON public_url_state;
CREATE CONSTRAINT TRIGGER ec_public_url_state_deferred2
    AFTER INSERT OR UPDATE OR DELETE ON public_url_state
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('public_url_revisions', 'public_url_state');

DROP TRIGGER IF EXISTS ec_smtp_state_deferred ON smtp_config_revisions;
CREATE CONSTRAINT TRIGGER ec_smtp_state_deferred
    AFTER INSERT OR UPDATE OR DELETE ON smtp_config_revisions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('smtp_config_revisions', 'smtp_config_state');

DROP TRIGGER IF EXISTS ec_smtp_state_deferred2 ON smtp_config_state;
CREATE CONSTRAINT TRIGGER ec_smtp_state_deferred2
    AFTER INSERT OR UPDATE OR DELETE ON smtp_config_state
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('smtp_config_revisions', 'smtp_config_state');

-- ===================== C. CORRECTIONS: invite / reset lifecycle =====================

-- C5 (corrected). Invite lifecycle.
--   * Insert must be pending_delivery with sent_at/consumed_at NULL.
--   * sent_at and consumed_at are immutable once set.
--   * expires_at is immutable EXCEPT for the one legal pending -> active
--     (delivery) transition, where it MUST be set to exactly sent_at + 72h.
--   * Legal transitions (synchronous-send contract):
--       pending_delivery -> active | delivery_failed | revoked
--       active           -> consumed | revoked
--       delivery_failed  -> (terminal; NOT revocable as if retryable)
--       revoked          -> (terminal)
--       consumed         -> (terminal)
--   * active requires sent_at IS NOT NULL; pending requires sent_at/consumed_at
--     NULL (no delivery activation semantics while pending).
CREATE OR REPLACE FUNCTION ec_invite_lifecycle()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'pending_delivery' THEN
            RAISE EXCEPTION 'new invites must start in pending_delivery';
        END IF;
        IF NEW.sent_at IS NOT NULL THEN
            RAISE EXCEPTION 'new invites cannot be marked sent';
        END IF;
        IF NEW.consumed_at IS NOT NULL THEN
            RAISE EXCEPTION 'new invites cannot be marked consumed';
        END IF;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        -- Immutable identity / snapshot fields.
        IF NEW.token_hash IS DISTINCT FROM OLD.token_hash THEN
            RAISE EXCEPTION 'invite token_hash is immutable';
        END IF;
        IF NEW.recipient_email IS DISTINCT FROM OLD.recipient_email THEN
            RAISE EXCEPTION 'invite recipient_email is immutable';
        END IF;
        IF NEW.recipient_role IS DISTINCT FROM OLD.recipient_role THEN
            RAISE EXCEPTION 'invite recipient_role is immutable';
        END IF;
        IF NEW.creator_id IS DISTINCT FROM OLD.creator_id THEN
            RAISE EXCEPTION 'invite creator_id is immutable';
        END IF;
        IF NEW.quota_policy_version_id IS DISTINCT FROM OLD.quota_policy_version_id
           OR NEW.url_revision_id IS DISTINCT FROM OLD.url_revision_id
           OR NEW.smtp_revision_id IS DISTINCT FROM OLD.smtp_revision_id THEN
            RAISE EXCEPTION 'invite snapshot FKs are immutable';
        END IF;

        -- sent_at / consumed_at immutable once set; pending must not carry them.
        IF NEW.sent_at IS DISTINCT FROM OLD.sent_at THEN
            IF OLD.sent_at IS NOT NULL THEN
                RAISE EXCEPTION 'invite sent_at is immutable once set';
            END IF;
            IF NEW.sent_at IS NULL THEN
                RAISE EXCEPTION 'invite sent_at cannot be cleared';
            END IF;
        END IF;
        IF NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
            RAISE EXCEPTION 'invite consumed_at is immutable once set';
        END IF;

        -- expires_at immutable except during the pending -> active delivery
        -- transition, where it must become exactly sent_at + 72h.
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            IF NOT (OLD.state = 'pending_delivery' AND NEW.state = 'active') THEN
                RAISE EXCEPTION
                    'invite expires_at is immutable except on the pending->active '
                    'delivery transition';
            END IF;
            IF NEW.expires_at IS DISTINCT FROM (NEW.sent_at + INTERVAL '72 hours') THEN
                RAISE EXCEPTION
                    'an active invite must expire exactly 72 hours after sent_at';
            END IF;
        END IF;

        -- Legal transitions.
        IF OLD.state = 'pending_delivery' AND NEW.state NOT IN ('active', 'delivery_failed', 'revoked') THEN
            RAISE EXCEPTION 'illegal transition % -> %', OLD.state, NEW.state;
        END IF;
        IF OLD.state = 'active' AND NEW.state NOT IN ('consumed', 'revoked') THEN
            RAISE EXCEPTION 'illegal transition % -> %', OLD.state, NEW.state;
        END IF;
        -- delivery_failed is terminal and must NOT be treated as a retryable/
        -- revocable state (it is a final failed-delivery record).
        IF OLD.state IN ('delivery_failed', 'revoked', 'consumed') THEN
            RAISE EXCEPTION 'state % is terminal', OLD.state;
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_invite_lifecycle ON registration_invites;
CREATE TRIGGER ec_invite_lifecycle
    BEFORE INSERT OR UPDATE ON registration_invites
    FOR EACH ROW EXECUTE FUNCTION ec_invite_lifecycle();

-- C5b. DB backstop CHECKs for invite coherence (idempotent add).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'registration_invites_active_coherent'
    ) THEN
        ALTER TABLE registration_invites
            ADD CONSTRAINT registration_invites_active_coherent
            CHECK (
                (state <> 'active' OR (sent_at IS NOT NULL
                    AND expires_at = sent_at + INTERVAL '72 hours'))
                AND (state <> 'pending_delivery' OR (sent_at IS NULL AND consumed_at IS NULL))
            );
    END IF;
END $$;

-- C7 (corrected). Reset token consistency: token_hash / user_id / expires_at
-- immutable (from 035), PLUS consumed_at is NULL -> timestamp and immutable after,
-- and attempt_count stays non-negative and monotonic. The "exactly at expiry is
-- expired" semantics are enforced by the Go model (IsExpired is inclusive of the
-- boundary); the DB only guarantees coherent, monotonic, non-negative metadata.
CREATE OR REPLACE FUNCTION ec_reset_consistency()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.token_hash IS DISTINCT FROM OLD.token_hash THEN
            RAISE EXCEPTION 'reset token_hash is immutable';
        END IF;
        IF NEW.user_id IS DISTINCT FROM OLD.user_id THEN
            RAISE EXCEPTION 'reset user_id is immutable';
        END IF;
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'reset expires_at is immutable';
        END IF;
        -- consumed_at: only NULL -> timestamp, then immutable.
        IF NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
            IF OLD.consumed_at IS NOT NULL THEN
                RAISE EXCEPTION 'reset consumed_at is immutable once set';
            END IF;
            IF NEW.consumed_at IS NULL THEN
                RAISE EXCEPTION 'reset consumed_at cannot be cleared once set';
            END IF;
        END IF;
        -- attempt_count: non-negative and monotonic.
        IF NEW.attempt_count < 0 THEN
            RAISE EXCEPTION 'reset attempt_count must be non-negative';
        END IF;
        IF NEW.attempt_count < OLD.attempt_count THEN
            RAISE EXCEPTION 'reset attempt_count must be monotonic (never decreases)';
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_reset_consistency ON password_reset_tokens;
CREATE TRIGGER ec_reset_consistency
    BEFORE INSERT OR UPDATE ON password_reset_tokens
    FOR EACH ROW EXECUTE FUNCTION ec_reset_consistency();

-- C7b. DB backstop CHECK for non-negative attempts (idempotent add).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'password_reset_tokens_attempts_nonneg'
    ) THEN
        ALTER TABLE password_reset_tokens
            ADD CONSTRAINT password_reset_tokens_attempts_nonneg
            CHECK (attempt_count >= 0);
    END IF;
END $$;

-- ===================== POSTCONDITION: no raw bearer token may remain =====================

-- After 036 (and 034/035 before it) the canonical password_reset_tokens table is
-- hash-only (token_hash). If a raw bearer-token column (`token`) survived 033a's
-- guard (e.g. on a drifted DB), FAIL CLOSED with an actionable message. Raw
-- tokens are never migrated or reinterpreted.
DO $$
DECLARE
    v_raw   boolean;
    v_hash  boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'password_reset_tokens'
          AND column_name = 'token'
    ) INTO v_raw;

    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'password_reset_tokens'
          AND column_name = 'token_hash'
    ) INTO v_hash;

    IF v_raw THEN
        RAISE EXCEPTION
            'POSTCONDITION FAILURE in migration 036: password_reset_tokens still '
            'contains a RAW bearer-token column (token). The canonical schema is '
            'hash-only (token_hash) and raw tokens must never be stored or '
            'migrated. Remediation: back up, drop the raw token column (or the '
            'legacy table), and re-run migration 034/036 so the canonical schema '
            'is authoritative. Do NOT attempt to reinterpret the raw token.';
    END IF;

    IF NOT v_hash THEN
        RAISE EXCEPTION
            'POSTCONDITION FAILURE in migration 036: password_reset_tokens is '
            'missing the canonical token_hash column. The canonical hash-only '
            'schema was not built. Remediation: re-run migration 034 so the '
            'canonical table is created before applying 036 corrections.';
    END IF;
END $$;
