-- Phase 1A Gate-1 remediation: managed-quota coherence safety (039a).
--
-- Forward-only corrective migration that fixes a defect introduced by 039's
-- deferred cross-table coherence trigger trg_user_quotas_managed_coherence().
--
-- 039's C2 function read COALESCE(NEW.user_id, OLD.user_id) and returned NEW.
-- Under PostgreSQL, a DELETE trigger has NO assigned NEW record, so referencing
-- NEW aborts EVERY delete on user_quotas. That made the otherwise-legal deletion
-- of a managed snapshot (returning a managed user to the zero-row pending state)
-- and the deletion of a legacy quota row impossible.
--
-- 039a fixes the function WITHOUT editing 039: it uses CREATE OR REPLACE
-- FUNCTION (which keeps the same function OID, so the trigger created by 039
-- stays bound to it). The corrected function branches on TG_OP: a DELETE uses
-- OLD.user_id and returns OLD; INSERT/UPDATE use NEW.user_id and return NEW. All
-- other 039 semantics are preserved verbatim:
--   * a managed user with zero user_quotas rows remains a legal pending state;
--   * legacy deletes work (validate_user_managed_quota_coherence returns
--     immediately for non-managed users);
--   * the DEFERRABLE INITIALLY DEFERRED commit-time coherence check still
--     validates the managed final state at COMMIT.
--
-- The paired .down.sql FAILS CLOSED (RAISE P0001, no destructive SQL) and must
-- never be applied; downgrades are handled by backup restore or a later forward
-- corrective migration.

-- ===========================================================================
-- C2 fix: corrected deferred coherence trigger function for user_quotas.
-- ===========================================================================
CREATE OR REPLACE FUNCTION trg_user_quotas_managed_coherence()
RETURNS trigger AS $$
DECLARE
    v_uid uuid;
BEGIN
    -- PostgreSQL DELETE triggers have NO assigned NEW record; referencing NEW
    -- would abort every delete. Branch on TG_OP so DELETE reads OLD and returns
    -- OLD, while INSERT/UPDATE read NEW and return NEW.
    IF TG_OP = 'DELETE' THEN
        v_uid := OLD.user_id;
    ELSE
        v_uid := NEW.user_id;
    END IF;

    IF v_uid IS NOT NULL THEN
        PERFORM validate_user_managed_quota_coherence(v_uid);
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Re-assert the trigger definition defensively. CREATE OR REPLACE above already
-- keeps the existing 039 trigger bound to the corrected function (same OID); the
-- DROP/CREATE here is idempotent and guards against a dropped trigger. The
-- trigger's DEFERRABLE INITIALLY DEFERRED semantics are unchanged.
DROP TRIGGER IF EXISTS trg_user_quotas_managed_coherence ON user_quotas;
CREATE CONSTRAINT TRIGGER trg_user_quotas_managed_coherence
    AFTER INSERT OR UPDATE OR DELETE ON user_quotas
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION trg_user_quotas_managed_coherence();
