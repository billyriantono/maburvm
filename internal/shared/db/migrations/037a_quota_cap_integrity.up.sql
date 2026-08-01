-- Phase 1A Gate-1 forward correction: quota-cap integrity (037a).
--
-- This migration runs AFTER 037 (which owns the columns, the original policy-cap
-- trigger, and the original fk_user_quota_policy_version constraint). It is
-- additive, idempotent, and forward-only. The paired .down.sql FAILS CLOSED and
-- must never be applied; downgrades are handled by backup restore or a later
-- forward corrective migration.
--
-- Remediation scope (reconciled audit findings):
--   A. FK drift preflight + safe validation. Before relying on (or adding) the
--      composite FK user_quotas(policy_id, policy_version) ->
--      quota_policy_versions(policy_id, version), detect non-null provenance
--      tuples with NO matching policy/version and FAIL with an actionable
--      exception. We never silently repair or null data. If the named FK already
--      exists (037 applied cleanly) we still run the orphan check; if it is
--      absent (historical/interrupted/variant state) we add it ONLY after a clean
--      preflight. Existence is decided by catalog inspection so this file never
--      touches 037's DDL.
--   B. Replace the dead policy-cap trigger. 037 created it as BEFORE INSERT OR
--      UPDATE; under 035's append-only guard policy versions cannot be UPDATEd,
--      so an UPDATE firing is dead/confusing. We DROP and recreate the trigger as
--      BEFORE INSERT ONLY (the function still stamps cap_revision_id and enforces
--      fail-closed publication under an active cap).
--   C. DB defense for managed snapshots. user_quotas rows with quota_mode='managed'
--      must carry a non-null cap_revision_id (in addition to the existing
--      policy/snapshot provenance and positive limits). Legacy rows keep NULL
--      quota_mode and are unaffected.
--   D. Platform cap control-plane hardening:
--        * revisions are immutable snapshots: forbid DELETE and forbid mutation
--          of the immutable columns (id, limits, revision, created_by,
--          created_at); only lifecycle columns (state, activated_at, retired_at)
--          may change, and only along candidate -> active -> retired;
--        * the singleton state/active-pointer must be coherent at transaction
--          end: exactly one active revision iff state='active' and the pointer
--          references it; no active pointer iff state='inactive'. Because
--          activation is multi-statement (retire previous + activate candidate +
--          move pointer), we use a DEFERRABLE INITIALLY DEFERRED CONSTRAINT
--          TRIGGER so the coherence check fires at COMMIT, not mid-transaction.
--
-- Everything is wrapped in the runner's single transaction; a raised exception
-- (orphan preflight, managed-cap requirement, coherence violation) aborts the
-- whole migration. Re-runs are safe (catalog-guarded, IF NOT EXISTS / DROP IF
-- EXISTS).

-- ===========================================================================
-- A) FK DRIFT PREFLIGHT + SAFE VALIDATION
-- ===========================================================================
-- Why: 037 added fk_user_quota_policy_version, but a DB that experienced an
-- interrupted or variant 037 may be missing it while already holding managed
-- snapshots. Adding the FK over dirty data would either fail opaquely or, worse,
-- silently coerce intent. We first count non-null provenance tuples that point at
-- no existing (policy_id, version). If any exist we RAISE immediately and the
-- transaction rolls back — no data is repaired or nulled.
DO $$
DECLARE
    v_orphans bigint;
BEGIN
    SELECT COUNT(*) INTO v_orphans
    FROM user_quotas u
    WHERE u.policy_id IS NOT NULL
      AND u.policy_version IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM quota_policy_versions v
          WHERE v.policy_id = u.policy_id
            AND v.version = u.policy_version
      );

    IF v_orphans > 0 THEN
        RAISE EXCEPTION
            'quota_integrity_orphan_provenance: % user_quotas row(s) reference a (policy_id, policy_version) with no matching quota_policy_versions row. Refusing to add/validate the composite FK. Manually reconcile or restore from backup before applying 037a.',
            v_orphans
            USING errcode = 'P0001';
    END IF;
END$$;

-- Add the named composite FK only if it is absent (catalog check). This is the
-- forward correction for a drifted DB; on a clean 037 DB the constraint already
-- exists and this is a no-op. We do NOT drop/recreate 037's constraint.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_name = 'fk_user_quota_policy_version'
          AND table_schema = 'public'
    ) THEN
        ALTER TABLE user_quotas
            ADD CONSTRAINT fk_user_quota_policy_version
            FOREIGN KEY (policy_id, policy_version)
            REFERENCES quota_policy_versions (policy_id, version);
    END IF;
END$$;

-- ===========================================================================
-- B) REPLACE DEAD POLICY-CAP TRIGGER (BEFORE INSERT ONLY)
-- ===========================================================================
-- Drop and recreate as BEFORE INSERT only. 035's append-only guard rejects
-- quota_policy_versions UPDATE before any row trigger would fire, so an UPDATE
-- firing here is dead code and a source of confusion. Publication remains
-- fail-closed: a new version requires an active cap and is stamped with that
-- cap's revision id.
DROP TRIGGER IF EXISTS trg_quota_policy_version_cap ON quota_policy_versions;

CREATE TRIGGER trg_quota_policy_version_cap
    BEFORE INSERT ON quota_policy_versions
    FOR EACH ROW EXECUTE FUNCTION trg_quota_policy_version_cap_check();

-- ===========================================================================
-- C) MANAGED SNAPSHOT REQUIRES cap_revision_id
-- ===========================================================================
-- Additive guard: a managed user_quotas row must carry a non-null cap_revision_id
-- in addition to its policy/snapshot provenance. Legacy rows (quota_mode IS NULL
-- or 'legacy') are exempt and remain permitted exactly as they are.
CREATE OR REPLACE FUNCTION trg_user_quota_managed_cap_check()
RETURNS trigger AS $$
BEGIN
    IF NEW.quota_mode = 'managed' THEN
        IF NEW.cap_revision_id IS NULL THEN
            RAISE EXCEPTION
                'quota_managed_cap_required: managed user_quotas row must carry a non-null cap_revision_id'
                USING errcode = 'P0001';
        END IF;
        IF NEW.policy_id IS NULL OR NEW.policy_version IS NULL THEN
            RAISE EXCEPTION
                'quota_managed_provenance_required: managed user_quotas row must carry policy_id and policy_version'
                USING errcode = 'P0001';
        END IF;
        IF NEW.max_vms <= 0 OR NEW.max_vcpu <= 0 OR NEW.max_ram_mb <= 0 OR NEW.max_disk_gb <= 0 THEN
            RAISE EXCEPTION
                'quota_managed_limits_required: managed user_quotas row must carry strictly positive limits'
                USING errcode = 'P0001';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_quota_managed_cap ON user_quotas;
CREATE TRIGGER trg_user_quota_managed_cap
    BEFORE INSERT OR UPDATE ON user_quotas
    FOR EACH ROW EXECUTE FUNCTION trg_user_quota_managed_cap_check();

-- ===========================================================================
-- D) PLATFORM CAP CONTROL-PLANE HARDENING
-- ===========================================================================

-- D1) Revisions are immutable snapshots: forbid DELETE and forbid mutation of the
--     immutable columns (including the snapshot NOTE, which is part of the
--     immutable record under the accepted contract). Only the lifecycle
--     timestamps (activated_at, retired_at) and the state column may change, and
--     only along candidate -> active -> retired with the matching timestamp
--     contract. Any other write is rejected. This supports the intended lifecycle
--     while preventing silent tampering of historical ceilings.
CREATE OR REPLACE FUNCTION trg_platform_cap_revision_immutable()
RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- Legal trigger semantics: return OLD so the row is removed; however the
        -- DELETE itself is rejected because revisions are immutable snapshots.
        RAISE EXCEPTION
            'platform_cap_revision_immutable: quota-cap revisions are immutable and must not be deleted (retire instead)'
            USING errcode = 'P0001';
    END IF;

    -- Allowed lifecycle transitions and their mandatory timestamp metadata.
    IF TG_OP = 'UPDATE' THEN
        IF OLD.state = 'candidate' AND NEW.state NOT IN ('candidate', 'active') THEN
            RAISE EXCEPTION 'platform_cap_revision_illegal_transition: candidate may only stay candidate or become active'
                USING errcode = 'P0001';
        END IF;
        IF OLD.state = 'active' AND NEW.state NOT IN ('active', 'retired') THEN
            RAISE EXCEPTION 'platform_cap_revision_illegal_transition: active may only stay active or become retired'
                USING errcode = 'P0001';
        END IF;
        IF OLD.state = 'retired' AND NEW.state <> 'retired' THEN
            RAISE EXCEPTION 'platform_cap_revision_illegal_transition: retired is terminal (no resurrection)'
                USING errcode = 'P0001';
        END IF;

        -- Lifecycle timestamp contract, enforcing legal transitions:
        --   * candidate: no activated_at, no retired_at
        --   * active:    must have activated_at, must NOT have retired_at
        --   * retired:   must have retired_at
        IF NEW.state = 'candidate' THEN
            IF NEW.activated_at IS NOT NULL OR NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: candidate must have no activated_at/retired_at'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'active' THEN
            IF NEW.activated_at IS NULL OR NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: active must have activated_at and no retired_at'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'retired' THEN
            IF NEW.retired_at IS NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: retired must have retired_at'
                    USING errcode = 'P0001';
            END IF;
        END IF;

        -- Immutable columns must not change (note is part of the immutable
        -- snapshot and is rejected from mutation).
        IF OLD.id <> NEW.id
           OR OLD.max_vms <> NEW.max_vms
           OR OLD.max_vcpu <> NEW.max_vcpu
           OR OLD.max_ram_mb <> NEW.max_ram_mb
           OR OLD.max_disk_gb <> NEW.max_disk_gb
           OR OLD.revision <> NEW.revision
           OR OLD.created_by IS DISTINCT FROM NEW.created_by
           OR OLD.created_at IS DISTINCT FROM NEW.created_at
           OR OLD.note IS DISTINCT FROM NEW.note THEN
            RAISE EXCEPTION 'platform_cap_revision_immutable: id/limits/revision/created_*/note are immutable'
                USING errcode = 'P0001';
        END IF;
    END IF;

    -- Deferred coherence is enforced separately; here we just validate the row
    -- being written so an INSERT cannot sneak in an internally inconsistent state.
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'candidate' AND (NEW.activated_at IS NOT NULL OR NEW.retired_at IS NOT NULL) THEN
            RAISE EXCEPTION 'platform_cap_revision_lifecycle: candidate must have no activated_at/retired_at'
                USING errcode = 'P0001';
        END IF;
        IF NEW.state = 'active' AND (NEW.activated_at IS NULL OR NEW.retired_at IS NOT NULL) THEN
            RAISE EXCEPTION 'platform_cap_revision_lifecycle: active must have activated_at and no retired_at'
                USING errcode = 'P0001';
        END IF;
        IF NEW.state = 'retired' AND NEW.retired_at IS NULL THEN
            RAISE EXCEPTION 'platform_cap_revision_lifecycle: retired must have retired_at'
                USING errcode = 'P0001';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_platform_cap_revision_immutable ON platform_quota_cap_revisions;
CREATE TRIGGER trg_platform_cap_revision_immutable
    BEFORE INSERT OR UPDATE OR DELETE ON platform_quota_cap_revisions
    FOR EACH ROW EXECUTE FUNCTION trg_platform_cap_revision_immutable();

-- D2) State/pointer coherence, validated at transaction END.
--     Activation is multi-statement (retire previous active + activate candidate
--     + move the singleton pointer). A statement-level check would fire between
--     statements and see a transiently-incoherent state, so we use a DEFERRABLE
--     INITIALLY DEFERRED CONSTRAINT TRIGGER, which fires at COMMIT. Invariants:
--       * state='active'  <=> exactly one revision has state='active'
--                          AND active_revision_id references that revision.
--       * state='inactive' <=> active_revision_id IS NULL
--                          AND zero revisions are 'active'.
--     A common validator is reused by BOTH the state-row trigger and the
--     revision trigger (defect 2): direct INSERT/UPDATE/DELETE on
--     platform_quota_cap_revisions can otherwise create zero/two/mismatched
--     active revisions without ever scheduling the end-of-transaction check.
CREATE OR REPLACE FUNCTION validate_platform_cap_coherence()
RETURNS void AS $$
DECLARE
    v_state          varchar(16);
    v_pointer        uuid;
    v_active_count   integer;
BEGIN
    -- The singleton state row MUST exist (defect 3: it is protected from
    -- deletion, but guard anyway).
    SELECT state, active_revision_id
      INTO v_state, v_pointer
      FROM platform_quota_cap_state
     WHERE singleton_key = 'A';

    IF NOT FOUND OR v_state IS NULL THEN
        RAISE EXCEPTION
            'platform_cap_coherence: singleton state row is missing or has a null state'
            USING errcode = 'P0001';
    END IF;

    SELECT COUNT(*) INTO v_active_count
      FROM platform_quota_cap_revisions
     WHERE state = 'active';

    IF v_state = 'active' THEN
        IF v_active_count <> 1 THEN
            RAISE EXCEPTION
                'platform_cap_coherence: state=active requires exactly one active revision (found %)', v_active_count
                USING errcode = 'P0001';
        END IF;
        IF v_pointer IS NULL OR NOT EXISTS (
            SELECT 1 FROM platform_quota_cap_revisions
             WHERE id = v_pointer AND state = 'active'
        ) THEN
            RAISE EXCEPTION
                'platform_cap_coherence: state=active requires active_revision_id to point at the single active revision'
                USING errcode = 'P0001';
        END IF;
    ELSIF v_state = 'inactive' THEN
        IF v_pointer IS NOT NULL THEN
            RAISE EXCEPTION
                'platform_cap_coherence: state=inactive requires a NULL active_revision_id'
                USING errcode = 'P0001';
        END IF;
        IF v_active_count <> 0 THEN
            RAISE EXCEPTION
                'platform_cap_coherence: state=inactive requires zero active revisions (found %)', v_active_count
                USING errcode = 'P0001';
        END IF;
    ELSE
        RAISE EXCEPTION 'platform_cap_coherence: unknown state %', v_state
            USING errcode = 'P0001';
    END IF;
END;
$$ LANGUAGE plpgsql;

-- Deferred state-row trigger: reuses the shared validator.
CREATE OR REPLACE FUNCTION trg_platform_cap_state_coherence()
RETURNS trigger AS $$
BEGIN
    PERFORM validate_platform_cap_coherence();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_platform_cap_state_coherence ON platform_quota_cap_state;
CREATE CONSTRAINT TRIGGER trg_platform_cap_state_coherence
    AFTER INSERT OR UPDATE ON platform_quota_cap_state
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION trg_platform_cap_state_coherence();

-- Deferred revision trigger (defect 2): any change to revisions must also defer
-- the coherence check to commit so that a multi-statement Activate/Retire (which
-- touches both tables across several statements) still succeeds.
CREATE OR REPLACE FUNCTION trg_platform_cap_revision_coherence()
RETURNS trigger AS $$
BEGIN
    PERFORM validate_platform_cap_coherence();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_platform_cap_revision_coherence ON platform_quota_cap_revisions;
CREATE CONSTRAINT TRIGGER trg_platform_cap_revision_coherence
    AFTER INSERT OR UPDATE OR DELETE ON platform_quota_cap_revisions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION trg_platform_cap_revision_coherence();

-- D3) Singleton state-row protection (defect 3):
--     * prevent deletion of the single state row;
--     * prevent mutation of singleton_key (always 'A');
--     * permit the control-plane repository updates to
--       active_revision_id / state / updated_by / updated_at.
CREATE OR REPLACE FUNCTION trg_platform_cap_state_protect()
RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION
            'platform_cap_state_protect: the singleton cap-state row must not be deleted'
            USING errcode = 'P0001';
    END IF;

    IF OLD.singleton_key <> NEW.singleton_key THEN
        RAISE EXCEPTION
            'platform_cap_state_protect: singleton_key is immutable'
            USING errcode = 'P0001';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_platform_cap_state_protect ON platform_quota_cap_state;
CREATE TRIGGER trg_platform_cap_state_protect
    BEFORE INSERT OR UPDATE OR DELETE ON platform_quota_cap_state
    FOR EACH ROW EXECUTE FUNCTION trg_platform_cap_state_protect();
