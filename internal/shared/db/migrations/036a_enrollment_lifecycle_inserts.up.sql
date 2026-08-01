-- 036a_enrollment_lifecycle_inserts: forward corrective follow-up to 036.
--
-- Precondition: 033, 033a, 033b, 034, 035, and 036 are recorded (036a sorts
-- AFTER 036, so it always applies after). This migration is purely corrective
-- and additive; it never edits 033-036 source and never drops the singleton
-- state rows or any table. It re-defines two guarding functions (CREATE OR
-- REPLACE) and adds one idempotent CHECK.
--
-- Corrects two Gate-1 gaps found in direct source review:
--
-- GAP 2 (reset INSERT lifecycle): 036's ec_reset_consistency() only enforced
--   consumed_at/attempt semantics on UPDATE. Reset records MUST be inserted
--   initially unconsumed. This migration adds an INSERT branch enforcing:
--     * consumed_at IS NULL on insert (must start unconsumed),
--     * attempt_count = 0 on insert (no prior attempts),
--     * last_attempt_at IS NULL on insert (no prior attempt timestamp),
--   and a coherence rule (both INSERT and UPDATE): a non-null last_attempt_at
--   must correspond to attempt_count > 0 (a timestamp with zero attempts is
--   incoherent). We do NOT require attempt_count > 0 => last_attempt_at NOT
--   NULL, to avoid over-constraining accepted service behavior (the service
--   always sets both together). Hash-only token semantics and the exact-expiry
--   rejection remain a Go-model concern and are untouched here.
--
-- GAP 3 (pending invite expiry is NOT a security boundary): the active invite
--   lifetime is exactly 72h from successful delivery (sent_at + 72h), enforced
--   by 036's registration_invites_active_coherent CHECK and by the Go model
--   IsConsumable() which requires state = 'active'. A pending_delivery record
--   therefore can NEVER be consumed regardless of its (soft, pre-delivery) TTL
--   expires_at, and at delivery MarkInviteSent resets expires_at to
--   sent_at + 72h, so a stale pre-delivery expiry can never become the active
--   security expiry (the active_coherent CHECK would reject it). We pin the
--   pending structural contract with an explicit idempotent CHECK so any future
--   regression (a pending row carrying sent_at/consumed_at) is rejected at the
--   DB layer, and document the proof that only the activation expiry is
--   security-relevant. expires_at stays NOT NULL for 034 compatibility.

-- ===================== GAP 2: reset token INSERT invariants =====================

CREATE OR REPLACE FUNCTION ec_reset_consistency()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- Reset tokens must be born unconsumed with no attempt history.
        IF NEW.consumed_at IS NOT NULL THEN
            RAISE EXCEPTION 'reset token must be inserted unconsumed (consumed_at must be NULL)';
        END IF;
        IF NEW.attempt_count IS NULL OR NEW.attempt_count <> 0 THEN
            RAISE EXCEPTION 'reset token must be inserted with attempt_count = 0';
        END IF;
        IF NEW.last_attempt_at IS NOT NULL THEN
            RAISE EXCEPTION 'reset token must be inserted with last_attempt_at NULL';
        END IF;
    END IF;

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

    -- Coherence (both INSERT and UPDATE): a non-null last_attempt_at must
    -- correspond to a positive attempt_count. We do NOT force the reverse so we
    -- do not over-constrain accepted service behavior.
    IF NEW.last_attempt_at IS NOT NULL AND (NEW.attempt_count IS NULL OR NEW.attempt_count <= 0) THEN
        RAISE EXCEPTION 'reset last_attempt_at must correspond to attempt_count > 0';
    END IF;

    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_reset_consistency ON password_reset_tokens;
CREATE TRIGGER ec_reset_consistency
    BEFORE INSERT OR UPDATE ON password_reset_tokens
    FOR EACH ROW EXECUTE FUNCTION ec_reset_consistency();

-- ===================== GAP 3: pending invite structural contract =====================
-- A pending_delivery invite MUST carry no delivery/sent/consumed markers and its
-- (soft, pre-delivery) expires_at must be after created_at. This makes explicit
-- that a pending record can never satisfy the active coherence CHECK and is
-- therefore never consumable. The security-relevant expiry is the ACTIVE expiry
-- (sent_at + 72h), enforced by registration_invites_active_coherent (036).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'registration_invites_pending_contract'
    ) THEN
        ALTER TABLE registration_invites
            ADD CONSTRAINT registration_invites_pending_contract
            CHECK (
                state <> 'pending_delivery'
                OR (sent_at IS NULL AND consumed_at IS NULL AND expires_at > created_at)
            );
    END IF;
END $$;
