-- Customer teams: projects group an account's VMs, and members are other
-- accounts the owner lets in with a role. Every column added to an existing
-- table is nullable or defaulted, so existing rows read as before.
CREATE TABLE IF NOT EXISTS projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_account_name ON projects (account_id, lower(name));

ALTER TABLE vms ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_vms_project_id ON vms (project_id) WHERE project_id IS NOT NULL;

-- user_id is NULL while the invitation is open. vm_ids and project_ids both
-- empty means every VM of the account.
CREATE TABLE IF NOT EXISTS account_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    role VARCHAR(16) NOT NULL,
    vm_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    project_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    invite_token_hash VARCHAR(64),
    invite_expires_at TIMESTAMPTZ,
    invited_by UUID,
    accepted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_members_account_email ON account_members (account_id, lower(email));
CREATE INDEX IF NOT EXISTS idx_account_members_user ON account_members (user_id) WHERE user_id IS NOT NULL;
-- One membership per person and account, whatever address it was sent to.
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_members_account_user ON account_members (account_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_members_token ON account_members (invite_token_hash) WHERE invite_token_hash IS NOT NULL;

-- An account made by accepting an invitation works in the team that invited
-- it; it orders nothing of its own until staff clear this.
ALTER TABLE users ADD COLUMN IF NOT EXISTS member_only BOOLEAN NOT NULL DEFAULT FALSE;

-- Profile details a customer edits themselves.
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone VARCHAR(40) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS company VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS locale VARCHAR(16) NOT NULL DEFAULT '';

-- App catalog: recipes staff publish to every customer, with the inputs a
-- customer fills in at order time.
ALTER TABLE recipes ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE recipes ADD COLUMN IF NOT EXISTS inputs JSONB NOT NULL DEFAULT '[]'::jsonb;
CREATE INDEX IF NOT EXISTS idx_recipes_public ON recipes (is_public) WHERE is_public;

-- A customer's request for one more address on a VM, decided by staff or
-- granted at once, depending on the portal settings.
CREATE TABLE IF NOT EXISTS ip_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family VARCHAR(4) NOT NULL,
    reason VARCHAR(500) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    address VARCHAR(64) NOT NULL DEFAULT '',
    note VARCHAR(500) NOT NULL DEFAULT '',
    decided_by UUID,
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ip_requests_status ON ip_requests (status, created_at);
CREATE INDEX IF NOT EXISTS idx_ip_requests_vm ON ip_requests (vm_id, created_at DESC);

-- More per-plan allowances (nil = no limit, 0 = none) and the display prices
-- of the extras, in the plan's currency.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_images INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_extra_disks INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_floating_ips INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS addon_prices JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Hourly averages of VM usage, kept far longer than the raw samples.
CREATE TABLE IF NOT EXISTS vm_metrics_hourly (
    vm_id UUID NOT NULL,
    bucket TIMESTAMPTZ NOT NULL,
    samples INTEGER NOT NULL DEFAULT 0,
    cpu_usage DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_usage DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_used_bytes BIGINT NOT NULL DEFAULT 0,
    disk_read_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    disk_write_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    network_rx_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    network_tx_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (vm_id, bucket)
);
CREATE INDEX IF NOT EXISTS idx_vm_metrics_hourly_bucket ON vm_metrics_hourly (bucket);
