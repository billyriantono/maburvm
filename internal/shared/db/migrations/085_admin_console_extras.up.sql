-- Admin console extras. Every change adds; nothing existing is rewritten.

-- A VM locked against deletion and reinstall: '' (not locked), 'owner' (the
-- customer locked it; the billing system may still remove it) or 'staff'
-- (nothing removes it until staff unlock it). Tags are the customer's labels.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS delete_lock VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE vms ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'::jsonb;
DO $$ BEGIN
    ALTER TABLE vms ADD CONSTRAINT vms_delete_lock_check CHECK (delete_lock IN ('', 'owner', 'staff'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Hypervisor groups: a free-form label on a node ("nvme", "dedicated-cpu").
-- A plan naming a group places its VMs only on nodes carrying it.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS node_group VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN IF NOT EXISTS node_group VARCHAR(64) NOT NULL DEFAULT '';

-- Marketing announcements skip customers who unsubscribed; service notices
-- (maintenance, incidents) still reach them.
ALTER TABLE users ADD COLUMN IF NOT EXISTS marketing_opt_out BOOLEAN NOT NULL DEFAULT FALSE;

-- Notes staff keep on a VM or an account. Customers never see them.
CREATE TABLE IF NOT EXISTS staff_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type VARCHAR(8) NOT NULL CHECK (subject_type IN ('vm', 'user')),
    subject_id UUID NOT NULL,
    body TEXT NOT NULL,
    author_id UUID,
    author_email VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_staff_notes_subject ON staff_notes (subject_type, subject_id, created_at DESC);

-- Announcements shown in the customer area, to everyone or to the owners of
-- VMs on chosen nodes, regions, plans or VMs, and optionally emailed.
CREATE TABLE IF NOT EXISTS announcements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title VARCHAR(200) NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    kind VARCHAR(16) NOT NULL DEFAULT 'service' CHECK (kind IN ('service', 'marketing')),
    audience JSONB NOT NULL DEFAULT '{"type": "all"}'::jsonb,
    starts_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ends_at TIMESTAMPTZ,
    emailed_at TIMESTAMPTZ,
    email_queued INTEGER NOT NULL DEFAULT 0,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_announcements_live ON announcements (starts_at DESC) WHERE deleted_at IS NULL;

-- One row per node and per storage pool per day: what was allocated against
-- what there is, and the day's average use. Kept for two years.
CREATE TABLE IF NOT EXISTS capacity_history (
    day DATE NOT NULL,
    scope VARCHAR(8) NOT NULL CHECK (scope IN ('node', 'pool')),
    scope_id TEXT NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    vms INTEGER NOT NULL DEFAULT 0,
    vcpu_allocated INTEGER NOT NULL DEFAULT 0,
    vcpu_total INTEGER NOT NULL DEFAULT 0,
    ram_allocated_mb BIGINT NOT NULL DEFAULT 0,
    ram_total_mb BIGINT NOT NULL DEFAULT 0,
    disk_allocated_gb BIGINT NOT NULL DEFAULT 0,
    disk_total_gb BIGINT NOT NULL DEFAULT 0,
    disk_used_gb BIGINT NOT NULL DEFAULT 0,
    cpu_usage REAL NOT NULL DEFAULT 0,
    memory_usage REAL NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (day, scope, scope_id)
);
