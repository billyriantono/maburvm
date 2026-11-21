-- Moving VMs between nodes, and draining a node.

-- Each node's migration identity as its agent last reported it: its migration
-- key (accepted on the other nodes) and its SSH host keys (pinned on them).
-- Written and read by table name, so a full-row save of a node cannot clear it.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS migration_public_key TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS ssh_host_keys TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS migration_identity_at TIMESTAMPTZ;

-- A drain moves every VM off a node, a few at a time. Not tied to the nodes
-- row by a foreign key: the report outlives the node.
CREATE TABLE IF NOT EXISTS node_drains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id UUID NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'running',
    concurrency INT NOT NULL DEFAULT 1,
    destinations JSONB NOT NULL DEFAULT '{}'::jsonb,
    requested_by UUID,
    job_id BIGINT,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_node_drains_node ON node_drains (node_id, started_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_node_drains_one_running ON node_drains (node_id) WHERE status IN ('running', 'aborting');

-- One move of one VM, from request to final state. Rows of a drain carry its
-- id; a VM a drain could not move gets a row with status 'skipped' and why.
CREATE TABLE IF NOT EXISTS vm_migrations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL,
    source_node_id UUID NOT NULL,
    dest_node_id UUID,
    dest_pool_id UUID,
    drain_id UUID,
    mode VARCHAR(16) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'queued',
    phase VARCHAR(200) NOT NULL DEFAULT '',
    bandwidth_mbps INT NOT NULL DEFAULT 0,
    bytes_total BIGINT NOT NULL DEFAULT 0,
    bytes_done BIGINT NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    requested_by UUID,
    job_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_vm_migrations_vm ON vm_migrations (vm_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_vm_migrations_drain ON vm_migrations (drain_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_migrations_one_active ON vm_migrations (vm_id) WHERE status IN ('queued', 'running', 'cancelling');
