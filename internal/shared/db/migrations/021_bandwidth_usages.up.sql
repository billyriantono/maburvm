-- Per-VM monthly bandwidth accounting (Virtualizor parity). One row per VM per
-- billing period (calendar month). The metrics collector samples cumulative
-- rx/tx counters and accumulates deltas here; quota_bytes mirrors the VM's
-- network bandwidth quota and `exceeded` flags overage.
CREATE TABLE IF NOT EXISTS bandwidth_usages (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id            UUID NOT NULL,
    node_id          UUID NOT NULL,
    period_start     DATE NOT NULL,
    period_end       DATE NOT NULL,
    rx_bytes         BIGINT NOT NULL DEFAULT 0,
    tx_bytes         BIGINT NOT NULL DEFAULT 0,
    total_bytes      BIGINT NOT NULL DEFAULT 0,
    quota_bytes      BIGINT NOT NULL DEFAULT 0,
    exceeded         BOOLEAN NOT NULL DEFAULT false,
    blocked_at       TIMESTAMPTZ,
    last_reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ
);

-- One row per (vm, period); the accumulator upserts on this key.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bw_vm_period ON bandwidth_usages(vm_id, period_start);
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_node ON bandwidth_usages(node_id);
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_deleted_at ON bandwidth_usages(deleted_at);
