-- Per-VM monthly bandwidth accounting (parity). One row per VM per
-- billing period (calendar month). The metrics collector samples cumulative
-- rx/tx counters and accumulates deltas here; quota_bytes mirrors the VM's
-- network bandwidth quota and `exceeded` flags overage.
--
-- Written idempotently: a bandwidth_usages table may already exist (the model +
-- repository predated this migration), possibly missing columns, so we create
-- the base table then add every column with IF NOT EXISTS to bring it up to spec.
CREATE TABLE IF NOT EXISTS bandwidth_usages (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id        UUID NOT NULL,
    node_id      UUID NOT NULL,
    period_start DATE NOT NULL,
    period_end   DATE NOT NULL
);

ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS rx_bytes         BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS tx_bytes         BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS total_bytes      BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS quota_bytes      BIGINT NOT NULL DEFAULT 0;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS exceeded         BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS blocked_at       TIMESTAMPTZ;
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS last_reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS deleted_at       TIMESTAMPTZ;

-- One row per (vm, period); the accumulator upserts on this key.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bw_vm_period ON bandwidth_usages(vm_id, period_start);
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_node ON bandwidth_usages(node_id);
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_deleted_at ON bandwidth_usages(deleted_at);
