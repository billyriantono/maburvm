-- VPS Plans (flavors): named resource bundles users select at VM creation.
CREATE TABLE IF NOT EXISTS plans (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           VARCHAR(100) NOT NULL,
    cpu            INTEGER NOT NULL,
    ram            INTEGER NOT NULL,          -- MB
    disk           INTEGER NOT NULL,          -- GB
    bandwidth_mbps INTEGER NOT NULL DEFAULT 0, -- 0 = unlimited
    description    TEXT,
    is_active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_name_unique ON plans(name) WHERE deleted_at IS NULL;
