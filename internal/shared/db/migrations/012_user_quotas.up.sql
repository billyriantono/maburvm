-- Per-user resource quotas. A value of 0 means unlimited for that dimension.
CREATE TABLE IF NOT EXISTS user_quotas (
    user_id     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    max_vms     INTEGER NOT NULL DEFAULT 0,
    max_vcpu    INTEGER NOT NULL DEFAULT 0,
    max_ram_mb  INTEGER NOT NULL DEFAULT 0,
    max_disk_gb INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
