-- One recovery of one VM from a node that went down onto another node. The row
-- outlives the job: until the old node's copy is removed it is what tells that
-- node, when it comes back, not to run the VM (old_copy).
CREATE TABLE IF NOT EXISTS vm_recoveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL,
    source_node_id UUID NOT NULL,
    dest_node_id UUID NOT NULL,
    -- shared: defined on another member of its shared pool, same disks.
    -- backup: restored from a backup onto the destination.
    method VARCHAR(16) NOT NULL,
    backup_id UUID,
    start_vm BOOLEAN NOT NULL DEFAULT TRUE,
    new_address BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(16) NOT NULL DEFAULT 'queued',
    phase VARCHAR(200) NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    -- moved: the VM's records point at the destination.
    moved BOOLEAN NOT NULL DEFAULT FALSE,
    -- old_copy: '' nothing to do, pending (the old node has not been told),
    -- fenced (told, and its copy is shut off), running (told, and its copy
    -- runs), removed.
    old_copy VARCHAR(16) NOT NULL DEFAULT '',
    requested_by UUID,
    job_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_vm_recoveries_vm ON vm_recoveries (vm_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_vm_recoveries_source ON vm_recoveries (source_node_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_recoveries_one_active ON vm_recoveries (vm_id) WHERE status IN ('queued', 'running');
