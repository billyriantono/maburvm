-- Phase 1A Gate-1 managed quota + configurable platform-cap remediation.
--
-- This migration builds ON TOP of 033 (quota-policy foundation) and 034/035
-- (enrollment control plane). It is additive and forward-only: the paired
-- .down.sql intentionally FAILS CLOSED and must never be applied; downgrades
-- are handled by backup restore or a forward corrective migration.
--
-- What this migration introduces:
--   1. A user-level legacy/managed marker on `users` (reusing the `quota_mode`
--      enum from 033) defaulted to 'legacy' for every existing row. This lets a
--      managed user WITHOUT a user_quotas row be distinguished from a legacy user
--      who simply has no row (both would otherwise look like "zero = unlimited").
--   2. Immutable, administrator-configurable platform quota-cap revisions with a
--      candidate -> active -> retired lifecycle, and a typed singleton active
--      pointer. No default cap values are seeded (enrollment/assignment stay
--      unavailable until an admin publishes and activates one).
--   3. Each quota_policy_version is bound to the active cap revision
--      (cap_revision_id) and DB-enforced (trigger) to not exceed the active cap.
--      Pre-037 versions (cap_revision_id IS NULL) remain readable but cannot be
--      used for managed assignment under the new provenance contract.
--   4. A composite FK from managed user_quotas (policy_id, policy_version) to the
--      immutable quota_policy_versions, plus full snapshot provenance columns
--      (cap_revision_id) on user_quotas.
--
-- All new objects use IF NOT EXISTS / safe defaults so the migration is
-- re-runnable against a drifted live DB.

-- 1) user-level legacy/managed marker (enum already exists from 033) ----------
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_mode quota_mode NOT NULL DEFAULT 'legacy';

-- 2) platform quota-cap revisions (immutable snapshots) -----------------------
CREATE TABLE IF NOT EXISTS platform_quota_cap_revisions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    max_vms      integer NOT NULL CHECK (max_vms > 0),
    max_vcpu     integer NOT NULL CHECK (max_vcpu > 0),
    max_ram_mb   integer NOT NULL CHECK (max_ram_mb > 0),
    max_disk_gb  integer NOT NULL CHECK (max_disk_gb > 0),
    state        varchar(16) NOT NULL DEFAULT 'candidate'
                  CHECK (state IN ('candidate', 'active', 'retired')),
    revision     bigint NOT NULL UNIQUE,           -- monotonic, immutable snapshot number
    created_by   uuid,
    note         varchar(255),
    created_at   timestamptz NOT NULL DEFAULT NOW(),
    activated_at timestamptz,
    retired_at   timestamptz
);

CREATE INDEX IF NOT EXISTS idx_platform_quota_cap_revisions_state
    ON platform_quota_cap_revisions (state, revision DESC);

-- Typed singleton active pointer/state for the platform cap control plane.
CREATE TABLE IF NOT EXISTS platform_quota_cap_state (
    singleton_key      varchar(1) PRIMARY KEY DEFAULT 'A' CHECK (singleton_key = 'A'),
    active_revision_id uuid REFERENCES platform_quota_cap_revisions(id),
    state              varchar(16) NOT NULL DEFAULT 'inactive'
                        CHECK (state IN ('inactive', 'active')),
    updated_by         uuid,
    updated_at         timestamptz NOT NULL DEFAULT NOW()
);

-- Exactly one singleton row, initially inactive (no active cap).
INSERT INTO platform_quota_cap_state (singleton_key, state)
VALUES ('A', 'inactive')
ON CONFLICT (singleton_key) DO NOTHING;

-- 3) bind quota_policy_versions to the active cap revision --------------------
ALTER TABLE quota_policy_versions ADD COLUMN IF NOT EXISTS cap_revision_id uuid;
CREATE INDEX IF NOT EXISTS idx_quota_policy_versions_cap
    ON quota_policy_versions (cap_revision_id);

-- Trigger: every new (or changed) policy version must be published under an
-- active cap and must not exceed it. The trigger also stamps cap_revision_id so
-- the binding is authoritative and stable under concurrent cap activation.
CREATE OR REPLACE FUNCTION trg_quota_policy_version_cap_check()
RETURNS trigger AS $$
DECLARE
    v_cap_id      uuid;
    v_max_vms     integer;
    v_max_vcpu    integer;
    v_max_ram_mb  integer;
    v_max_disk_gb integer;
BEGIN
    SELECT r.id, r.max_vms, r.max_vcpu, r.max_ram_mb, r.max_disk_gb
      INTO v_cap_id, v_max_vms, v_max_vcpu, v_max_ram_mb, v_max_disk_gb
      FROM platform_quota_cap_state s
      JOIN platform_quota_cap_revisions r ON r.id = s.active_revision_id
     WHERE s.singleton_key = 'A'
       AND s.state = 'active'
       AND r.state = 'active';

    IF v_cap_id IS NULL THEN
        RAISE EXCEPTION 'quota_cap_required: no active platform quota cap; cannot publish policy version'
            USING errcode = 'P0001';
    END IF;

    IF NEW.max_vms > v_max_vms OR NEW.max_vcpu > v_max_vcpu
       OR NEW.max_ram_mb > v_max_ram_mb OR NEW.max_disk_gb > v_max_disk_gb THEN
        RAISE EXCEPTION 'quota_cap_exceeded: policy version limits exceed the active platform cap'
            USING errcode = 'P0001';
    END IF;

    NEW.cap_revision_id := v_cap_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_quota_policy_version_cap ON quota_policy_versions;
CREATE TRIGGER trg_quota_policy_version_cap
    BEFORE INSERT OR UPDATE ON quota_policy_versions
    FOR EACH ROW EXECUTE FUNCTION trg_quota_policy_version_cap_check();

-- 4) composite FK from managed user_quotas to the immutable policy version -----
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS cap_revision_id uuid;

-- Referenced columns are the unique (policy_id, version) of quota_policy_versions.
-- Both user_quotas.policy_id and policy_version are nullable, so legacy rows
-- (NULL provenance) are exempt from the FK check. PostgreSQL has no
-- "ADD CONSTRAINT IF NOT EXISTS", so guard it with a DO block.
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
