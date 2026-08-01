-- Phase 1A Gate-1 forward correction: managed snapshot data-integrity (039).
--
-- This migration runs AFTER 033 (quota-policy foundation), 037 (managed quota +
-- cap remediation), 037a (quota-cap integrity), 037b (platform cap lifecycle
-- alignment) and 038 (invite consumption alignment). It is additive, idempotent,
-- and forward-only. The paired .down.sql FAILS CLOSED and must never be applied;
-- downgrades are handled by backup restore or a later forward corrective
-- migration.
--
-- Problem statement: 037a guarantees a managed user_quotas row carries full
-- managed provenance (policy_id, policy_version, cap_revision_id) and positive
-- limits, but it does NOT guarantee the snapshot's four limits and cap exactly
-- equal the immutable referenced quota_policy_versions(policy_id, version). A
-- direct SQL write (or a future code path) could tamper with a managed snapshot
-- while still satisfying 037a. 039 closes that gap and adds cross-table
-- coherence between users.quota_mode (authoritative) and user_quotas.
--
-- Scope (reconciled audit finding for Gate-1 039):
--   A. ROW TRIGGER (BEFORE INSERT OR UPDATE on user_quotas, managed rows only):
--      load the referenced immutable quota_policy_versions(policy_id, version)
--      and require EXACT equality of the four limits AND cap_revision_id. This
--      rejects direct SQL limit/cap tampering of a managed snapshot. It does NOT
--      require cap to equal the *current* active cap; a historical snapshot
--      validly remains bound to the cap revision stamped on its immutable policy
--      version (version.cap_revision_id). Provenance/positive-limit requirements
--      already enforced by 037a's trg_user_quota_managed_cap_check remain in
--      force and are re-asserted here for a single source of truth.
--   B. MIGRATION PREFLIGHT (actionable, fail-closed): reject extant managed-user
--      inconsistent data. Any managed user (users.quota_mode='managed') with a
--      legacy row, a malformed managed row, a multiplicty > 1 of rows, or a
--      snapshot that differs from its referenced immutable policy version is
--      rejected with an actionable exception. We NEVER repair or null data. A
--      managed user with ZERO user_quotas rows is explicitly permitted (the
--      application fails closed on the read path for such a pending user).
--   C. DEFERRED CROSS-TABLE COHERENCE (INITIALLY DEFERRED): at COMMIT, for a user
--      whose authoritative users.quota_mode='managed':
--        * zero quota rows is permitted (pending; read path fails closed);
--        * otherwise exactly ONE row must exist, it must be quota_mode='managed',
--          and the normal row trigger (A) must validate it (limits + cap exactly
--          equal the referenced immutable version);
--        * any legacy row, multiple rows, malformed/mismatched managed snapshot
--          must be rejected.
--      Legacy users are NOT constrained and retain missing/zero unlimited
--      semantics. Triggers are DEFERRABLE INITIALLY DEFERRED so a valid outer
--      transaction that flips a user to managed and then writes its snapshot
--      before commit is NOT falsely rejected (the check fires at COMMIT on the
--      final, coherent state). This preserves all 037/037a/037b/038 semantics.
--
-- Everything is wrapped in the runner's single transaction; a raised exception
-- (preflight drift, tamper rejection, coherence violation) aborts the whole
-- migration. Re-runs are safe (catalog-guarded, CREATE OR REPLACE / DROP IF
-- EXISTS).

-- ===========================================================================
-- A) ROW TRIGGER: managed snapshot must exactly equal its immutable policy version
-- ===========================================================================
CREATE OR REPLACE FUNCTION trg_user_quota_managed_snapshot_integrity()
RETURNS trigger AS $$
DECLARE
    v_pv quota_policy_versions%ROWTYPE;
BEGIN
    -- Legacy rows are exempt; 037a already enforces mandatory managed provenance
    -- and positive limits, but we re-assert here so this trigger is a complete
    -- single source of truth for managed-snapshot data integrity.
    IF NEW.quota_mode = 'managed' THEN
        IF NEW.policy_id IS NULL OR NEW.policy_version IS NULL THEN
            RAISE EXCEPTION
                'quota_managed_provenance_required: managed user_quotas row must carry policy_id and policy_version'
                USING errcode = 'P0001';
        END IF;
        IF NEW.cap_revision_id IS NULL THEN
            RAISE EXCEPTION
                'quota_managed_cap_required: managed user_quotas row must carry a non-null cap_revision_id'
                USING errcode = 'P0001';
        END IF;
        IF NEW.max_vms <= 0 OR NEW.max_vcpu <= 0 OR NEW.max_ram_mb <= 0 OR NEW.max_disk_gb <= 0 THEN
            RAISE EXCEPTION
                'quota_managed_limits_required: managed user_quotas row must carry strictly positive limits'
                USING errcode = 'P0001';
        END IF;

        -- Load the referenced immutable policy version. (policy_id, version) is
        -- UNIQUE, so at most one row is returned.
        SELECT * INTO v_pv
          FROM quota_policy_versions
         WHERE policy_id = NEW.policy_id
           AND version = NEW.policy_version;

        IF NOT FOUND THEN
            RAISE EXCEPTION
                'quota_managed_version_missing: managed snapshot references a (policy_id, version) with no matching immutable quota_policy_versions row'
                USING errcode = 'P0001';
        END IF;

        -- Exact equality of the four limits: reject direct limit tampering.
        IF v_pv.max_vms <> NEW.max_vms OR v_pv.max_vcpu <> NEW.max_vcpu
           OR v_pv.max_ram_mb <> NEW.max_ram_mb OR v_pv.max_disk_gb <> NEW.max_disk_gb THEN
            RAISE EXCEPTION
                'quota_managed_snapshot_mismatch: managed snapshot limits must exactly equal the referenced immutable quota_policy_versions row (rejecting direct limit tampering)'
                USING errcode = 'P0001';
        END IF;

        -- Exact equality of cap_revision_id: reject direct cap tampering. We
        -- compare against the cap stamped on the immutable version, NOT the
        -- current active cap, so a historical snapshot validly remains bound to
        -- the cap revision under which it was published.
        IF v_pv.cap_revision_id IS DISTINCT FROM NEW.cap_revision_id THEN
            RAISE EXCEPTION
                'quota_managed_cap_mismatch: managed snapshot cap_revision_id must exactly equal the cap_revision_id stamped on the referenced immutable policy version (rejecting direct cap tampering)'
                USING errcode = 'P0001';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_quota_managed_snapshot_integrity ON user_quotas;
CREATE TRIGGER trg_user_quota_managed_snapshot_integrity
    BEFORE INSERT OR UPDATE ON user_quotas
    FOR EACH ROW EXECUTE FUNCTION trg_user_quota_managed_snapshot_integrity();

-- ===========================================================================
-- B) MIGRATION PREFLIGHT: reject extant managed-user inconsistent data
-- ===========================================================================
-- Why: a DB that already holds managed users with legacy rows, malformed managed
-- rows, multiple rows, or snapshots that drifted from their immutable policy
-- version must NOT be silently accepted. We count offending managed users and
-- RAISE immediately (actionable). We never repair or null data. A managed user
-- with ZERO user_quotas rows is permitted (pending; app fails closed). Legacy
-- users are never inspected here.
DO $$
DECLARE
    v_bad bigint;
BEGIN
    SELECT COUNT(DISTINCT u.id) INTO v_bad
    FROM users u
    WHERE u.quota_mode = 'managed'
      AND (
            -- More than one row for a managed user is incoherent (must be exactly
            -- one, or zero while pending).
            (SELECT COUNT(*) FROM user_quotas q WHERE q.user_id = u.id) > 1
            OR EXISTS (
                SELECT 1 FROM user_quotas q
                WHERE q.user_id = u.id
                  AND (
                        -- A row that is not managed under a managed user (legacy).
                        q.quota_mode <> 'managed'
                        -- Missing mandatory managed provenance / cap.
                        OR q.policy_id IS NULL OR q.policy_version IS NULL OR q.cap_revision_id IS NULL
                        -- Non-positive (malformed) managed limits.
                        OR q.max_vms <= 0 OR q.max_vcpu <= 0 OR q.max_ram_mb <= 0 OR q.max_disk_gb <= 0
                        -- Snapshot that differs from its referenced immutable
                        -- policy version (limits and/or bound cap).
                        OR NOT EXISTS (
                            SELECT 1 FROM quota_policy_versions v
                            WHERE v.policy_id = q.policy_id
                              AND v.version = q.policy_version
                              AND v.max_vms = q.max_vms
                              AND v.max_vcpu = q.max_vcpu
                              AND v.max_ram_mb = q.max_ram_mb
                              AND v.max_disk_gb = q.max_disk_gb
                              AND v.cap_revision_id IS NOT DISTINCT FROM q.cap_revision_id
                        )
                  )
            )
          );

    IF v_bad > 0 THEN
        RAISE EXCEPTION
            'quota_snapshot_integrity_drift: % managed user(s) carry inconsistent user_quotas rows (legacy/malformed/multiple/mismatched snapshot). Refusing migration 039. Manually reconcile or restore from backup before applying.',
            v_bad
            USING errcode = 'P0001';
    END IF;
END$$;

-- ===========================================================================
-- C) DEFERRED CROSS-TABLE COHERENCE (users <-> user_quotas)
-- ===========================================================================
-- Shared validator: given a user_id, enforce managed coherence AT COMMIT.
--   * non-managed (legacy) users are not constrained (missing/zero unlimited).
--   * managed + zero rows  -> permitted (pending; read path fails closed).
--   * managed + >1 rows    -> rejected (must be exactly one).
--   * managed + 1 row      -> must be quota_mode='managed' and must exactly equal
--                             its referenced immutable policy version (the same
--                             contract enforced by the row trigger in A).
CREATE OR REPLACE FUNCTION validate_user_managed_quota_coherence(p_user_id uuid)
RETURNS void AS $$
DECLARE
    v_mode  quota_mode;
    v_count integer;
    v_row   user_quotas%ROWTYPE;
    v_pv    quota_policy_versions%ROWTYPE;
BEGIN
    SELECT quota_mode INTO v_mode FROM users WHERE id = p_user_id;
    IF v_mode IS DISTINCT FROM 'managed' THEN
        RETURN; -- legacy/unset users are not constrained
    END IF;

    SELECT COUNT(*) INTO v_count FROM user_quotas WHERE user_id = p_user_id;
    IF v_count = 0 THEN
        RETURN; -- pending managed user; read path fails closed in the application
    END IF;
    IF v_count > 1 THEN
        RAISE EXCEPTION
            'user_managed_quota_coherence: managed user % must have exactly one user_quotas row (found %)',
            p_user_id, v_count
            USING errcode = 'P0001';
    END IF;

    SELECT * INTO v_row FROM user_quotas WHERE user_id = p_user_id;

    -- The single row must itself be a managed snapshot (no legacy row for a
    -- managed user) and well-formed; otherwise reject.
    IF v_row.quota_mode <> 'managed' THEN
        RAISE EXCEPTION
            'user_managed_quota_coherence: managed user % has a legacy/mismatched user_quotas row',
            p_user_id
            USING errcode = 'P0001';
    END IF;
    IF v_row.policy_id IS NULL OR v_row.policy_version IS NULL OR v_row.cap_revision_id IS NULL
       OR v_row.max_vms <= 0 OR v_row.max_vcpu <= 0 OR v_row.max_ram_mb <= 0 OR v_row.max_disk_gb <= 0 THEN
        RAISE EXCEPTION
            'user_managed_quota_coherence: managed user % has a malformed snapshot row',
            p_user_id
            USING errcode = 'P0001';
    END IF;

    -- Re-validate against the referenced immutable policy version so any state
    -- not caught by the row trigger (e.g. pre-existing rows) is still rejected.
    SELECT * INTO v_pv
      FROM quota_policy_versions
     WHERE policy_id = v_row.policy_id
       AND version = v_row.policy_version;
    IF NOT FOUND THEN
        RAISE EXCEPTION
            'user_managed_quota_coherence: managed user % references a missing policy version',
            p_user_id
            USING errcode = 'P0001';
    END IF;
    IF v_pv.max_vms <> v_row.max_vms OR v_pv.max_vcpu <> v_row.max_vcpu
       OR v_pv.max_ram_mb <> v_row.max_ram_mb OR v_pv.max_disk_gb <> v_row.max_disk_gb
       OR v_pv.cap_revision_id IS DISTINCT FROM v_row.cap_revision_id THEN
        RAISE EXCEPTION
            'user_managed_quota_coherence: managed user % snapshot mismatches its immutable policy version',
            p_user_id
            USING errcode = 'P0001';
    END IF;
END;
$$ LANGUAGE plpgsql;

-- C1) Deferred trigger on users: when a user is (or becomes) managed, validate
--     its quota coherence at COMMIT. We only schedule validation when the NEW
--     row is managed; legacy flips are not constrained.
CREATE OR REPLACE FUNCTION trg_users_managed_quota_coherence()
RETURNS trigger AS $$
BEGIN
    IF NEW.quota_mode = 'managed' THEN
        PERFORM validate_user_managed_quota_coherence(NEW.id);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_managed_quota_coherence ON users;
CREATE CONSTRAINT TRIGGER trg_users_managed_quota_coherence
    AFTER INSERT OR UPDATE ON users
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION trg_users_managed_quota_coherence();

-- C2) Deferred trigger on user_quotas: any change (insert/update/delete) to a
--     quota row schedules the coherence check for its user at COMMIT. This is
--     what makes the zero-row/pending managed state legal while still rejecting
--     legacy/multiple/malformed/mismatched managed snapshots at transaction end.
CREATE OR REPLACE FUNCTION trg_user_quotas_managed_coherence()
RETURNS trigger AS $$
DECLARE
    v_uid uuid;
BEGIN
    v_uid := COALESCE(NEW.user_id, OLD.user_id);
    IF v_uid IS NOT NULL THEN
        PERFORM validate_user_managed_quota_coherence(v_uid);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_user_quotas_managed_coherence ON user_quotas;
CREATE CONSTRAINT TRIGGER trg_user_quotas_managed_coherence
    AFTER INSERT OR UPDATE OR DELETE ON user_quotas
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION trg_user_quotas_managed_coherence();
