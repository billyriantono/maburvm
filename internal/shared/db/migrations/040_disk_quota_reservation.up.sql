-- Phase 1A Gate-1 remediation: disk-quota reservation model + nonnegative protection (040).
--
-- Forward-only. The paired .down.sql FAILS CLOSED (RAISE P0001, no destructive
-- SQL) and must never be applied; downgrades are handled by backup restore or a
-- later forward corrective migration.
--
-- Scope (Gate-1 040):
--   A. PREFLIGHT (actionable, fail-closed): reject extant negative quota data in
--      user_quotas. A negative value is INVALID and must NEVER mean unlimited; we
--      refuse to silently repair or zero it. A legacy user with ZERO limits keeps
--      the unlimited semantics exactly as before — only STRICTLY NEGATIVE rows
--      are rejected.
--   B. NONNEGATIVE DB-LEVEL PROTECTION for all four quota fields
--      (max_vms, max_vcpu, max_ram_mb, max_disk_gb). This is a hard CHECK that
--      applies to legacy AND managed rows. It preserves the existing zero=unlimited
--      legacy semantics (zero is allowed) and the existing managed positive
--      constraints already enforced by migration 039's row trigger (which requires
--      managed limits > 0). The two layers are complementary, not conflicting.
--   C. DURABLE DISK-QUOTA RESERVATION TABLE (disk_quota_reservations) for pending
--      extra-disk admission. It associates a reservation with a user and a VM,
--      requires a strictly positive size, and supports an atomic
--      consumption/release lifecycle. There is NO TTL / automatic expiry: a
--      pending reservation intentionally overcounts (so it serializes concurrent
--      increases and never permits an agent-attached disk to bypass quota) until
--      it is explicitly consumed (on agent success) or released (on agent
--      failure). Only a final-DB-recording failure after agent success retains
--      the reservation fail-closed (handled in the application layer).

-- ===========================================================================
-- A) PREFLIGHT: reject extant negative quota data (never repair).
-- ===========================================================================
DO $$
DECLARE
    v_bad bigint;
BEGIN
    SELECT COUNT(*) INTO v_bad
      FROM user_quotas
     WHERE max_vms < 0
        OR max_vcpu < 0
        OR max_ram_mb < 0
        OR max_disk_gb < 0;

    IF v_bad > 0 THEN
        RAISE EXCEPTION
            'quota_negative_data: % user_quotas row(s) carry a negative limit '
            '(max_vms/max_vcpu/max_ram_mb/max_disk_gb). A negative limit is invalid '
            'and must NEVER be interpreted as unlimited. Refusing migration 040. '
            'Manually reconcile the negative values (zero = unlimited for legacy '
            'accounts) before applying this migration.',
            v_bad
            USING errcode = 'P0001';
    END IF;
END$$;

-- ===========================================================================
-- B) NONNEGATIVE CHECK for all four quota fields (legacy + managed).
-- ===========================================================================
ALTER TABLE user_quotas
    ADD CONSTRAINT chk_user_quotas_nonneg
    CHECK (
        max_vms     >= 0
        AND max_vcpu   >= 0
        AND max_ram_mb >= 0
        AND max_disk_gb >= 0
    );

-- ===========================================================================
-- C) DISK-QUOTA RESERVATION TABLE for pending extra-disk admission.
-- ===========================================================================
CREATE TABLE disk_quota_reservations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id),
    vm_id       uuid        NOT NULL REFERENCES vms (id),
    size_gb     integer     NOT NULL CHECK (size_gb > 0),
    status      text        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'consumed')),
    created_at  timestamptz NOT NULL DEFAULT NOW(),
    updated_at  timestamptz NOT NULL DEFAULT NOW(),
    consumed_at timestamptz
);

CREATE INDEX idx_disk_quota_reservations_user ON disk_quota_reservations (user_id);
CREATE INDEX idx_disk_quota_reservations_vm   ON disk_quota_reservations (vm_id);
