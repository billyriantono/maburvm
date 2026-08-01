-- 038_invite_consumption_lifecycle_alignment: forward-only Gate-1 correction.
--
-- Precondition: 033, 033a, 033b, 034, 035, 036, 036a are recorded (038 sorts
-- AFTER 036a, so it always applies after). This migration does NOT edit 036/036a;
-- it additively REPLACES the ec_invite_lifecycle guard (CREATE OR REPLACE) so the
-- single behavioral defect is corrected without altering already-applied source.
--
-- ORACLE BLOCKER: 036's ec_invite_lifecycle rejected EVERY change to consumed_at
-- (treating it as immutable once set, with no legal path to set it). That made
-- ConsumeInviteTx's active -> consumed transition impossible: the repo update
-- (state -> consumed, consumed_at -> now) was blocked by the trigger, so invites
-- could never be marked consumed. 038 fixes ONLY the consumed_at rule.
--
-- Exact contract preserved/added (everything else identical to 036/036a):
--   * INSERT: state must be pending_delivery; sent_at/consumed_at must be NULL.
--   * Immutable identity/snapshot fields: token_hash, recipient_email,
--     recipient_role, creator_id, quota/url/smtp snapshot FKs.
--   * sent_at immutable once set; cannot be cleared.
--   * expires_at immutable EXCEPT the one legal pending -> active delivery
--     transition, where it must equal exactly sent_at + 72h.
--   * Legal state transitions:
--       pending_delivery -> active | delivery_failed | revoked
--       active           -> consumed | revoked
--       delivery_failed  -> terminal (NOT revocable / retryable)
--       revoked          -> terminal
--       consumed         -> terminal
--   * consumed_at rule (THE FIX):
--       - on INSERT it must be NULL (unconsumed at birth);
--       - it may ONLY be set during the exact transition
--           old.state='active' AND new.state='consumed'
--           AND old.consumed_at IS NULL AND new.consumed_at IS NOT NULL;
--       - once set it is immutable forever (no further change allowed);
--       - any other attempted write of consumed_at is rejected, including:
--           * setting consumed_at on pending/delivery_failed/revoked rows,
--           * active -> consumed with a NULL consumed_at,
--           * clearing/overwriting an already-set consumed_at.
--   * A consumed transition must not alter sent_at, expires_at, or any immutable
--     identity/snapshot field (those rules still apply and would reject such a
--     change). delivery_failed stays terminal and non-retryable; pending/active
--     rows cannot become consumed without the successful-delivery (active)
--     prerequisite, because only active -> consumed is permitted.
--
-- This is compatible with the existing registration_invites_active_coherent and
-- registration_invites_pending_contract CHECKs (a consumed row trivially
-- satisfies both). No raw token, retry, or delivery semantics are weakened.

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

        -- sent_at immutable once set; cannot be cleared.
        IF NEW.sent_at IS DISTINCT FROM OLD.sent_at THEN
            IF OLD.sent_at IS NOT NULL THEN
                RAISE EXCEPTION 'invite sent_at is immutable once set';
            END IF;
            IF NEW.sent_at IS NULL THEN
                RAISE EXCEPTION 'invite sent_at cannot be cleared';
            END IF;
        END IF;

        -- consumed_at rule (THE FIX): only the exact active -> consumed
        -- transition may set it, and once set it is immutable forever.
        IF NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
            IF OLD.state = 'active' AND NEW.state = 'consumed'
               AND OLD.consumed_at IS NULL AND NEW.consumed_at IS NOT NULL THEN
                -- Legal consumption. Allowed; nothing else to enforce here.
                NULL;
            ELSIF OLD.consumed_at IS NOT NULL THEN
                RAISE EXCEPTION 'invite consumed_at is immutable once set';
            ELSIF NEW.consumed_at IS NULL THEN
                RAISE EXCEPTION 'invite consumed_at cannot be cleared';
            ELSE
                -- Any other attempt to write consumed_at (e.g. on
                -- pending/delivery_failed/revoked, or active->consumed with a
                -- NULL value) is rejected: consumption requires a successful
                -- delivery (active state) and a real timestamp.
                RAISE EXCEPTION
                    'invite consumed_at may only be set on the active -> consumed '
                    'transition with a non-null timestamp';
            END IF;
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
