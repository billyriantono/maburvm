-- Phase 1A: account quota-policy data foundation.
--
-- This migration is purely additive and does not alter the meaning of existing
-- user_quotas rows or existing plans:
--   * user_quotas gains a legacy/managed marker (default 'legacy') plus nullable
--     provenance columns for later managed-policy enforcement. Existing rows and
--     the zero-means-unlimited semantics are preserved.
--   * New quota_policies / quota_policy_versions tables model named, versioned,
--     immutable account quota policies. No seeded rows and no default policy are
--     created: enrollment stays unavailable until an admin publishes one.
--
-- All new tables/columns are created IF NOT EXISTS / with safe defaults so the
-- migration can be re-applied against a drifted live DB without error. The down
-- migration only drops objects introduced here; it never touches user_quotas
-- core columns, plans, or user data.

-- Enums -------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'quota_mode') THEN
        CREATE TYPE quota_mode AS ENUM ('legacy', 'managed');
    END IF;
END$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'quota_policy_lifecycle') THEN
        CREATE TYPE quota_policy_lifecycle AS ENUM ('active', 'deprecated');
    END IF;
END$$;

-- user_quotas: additive legacy/managed marker + nullable provenance ----------
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS quota_mode quota_mode NOT NULL DEFAULT 'legacy';
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS policy_id uuid;
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS policy_version integer;
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS policy_name varchar(100);
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS policy_assigned_at timestamptz;
ALTER TABLE user_quotas ADD COLUMN IF NOT EXISTS policy_assigned_by uuid;

-- quota_policies: named, lifecycle-managed policies -------------------------
CREATE TABLE IF NOT EXISTS quota_policies (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        varchar(100) NOT NULL UNIQUE,
    description text,
    lifecycle   quota_policy_lifecycle NOT NULL DEFAULT 'active',
    is_default  boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT NOW(),
    updated_at  timestamptz NOT NULL DEFAULT NOW()
);

-- At most one ACTIVE policy may be flagged as the default. Deprecated policies
-- must not participate in the default uniqueness, so we scope the partial index
-- to active policies only. There is no implicit "first active" default.
CREATE UNIQUE INDEX IF NOT EXISTS quota_policies_single_active_default
    ON quota_policies (is_default)
    WHERE is_default = true AND lifecycle = 'active';

-- quota_policy_versions: immutable, append-only limit sets -------------------
CREATE TABLE IF NOT EXISTS quota_policy_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id    uuid NOT NULL REFERENCES quota_policies(id) ON DELETE RESTRICT,
    version      integer NOT NULL,
    max_vms      integer NOT NULL CHECK (max_vms > 0),
    max_vcpu     integer NOT NULL CHECK (max_vcpu > 0),
    max_ram_mb   integer NOT NULL CHECK (max_ram_mb > 0),
    max_disk_gb  integer NOT NULL CHECK (max_disk_gb > 0),
    note         varchar(255),
    created_at   timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT quota_policy_versions_policy_version_uniq UNIQUE (policy_id, version)
);

CREATE INDEX IF NOT EXISTS idx_quota_policy_versions_policy
    ON quota_policy_versions (policy_id);
