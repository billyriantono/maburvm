-- Abuse and DDoS handling, traffic accounting, graph history and reverse DNS.
-- Ids are kept as text and compared as text: on older databases some id
-- columns are varchar rather than uuid.

-- Traffic accounting: the last cumulative counters read from each VM, so a
-- panel restart differences against them instead of losing the first sample.
CREATE TABLE IF NOT EXISTS vm_traffic_counters (
    vm_id TEXT PRIMARY KEY,
    rx_bytes BIGINT NOT NULL DEFAULT 0,
    tx_bytes BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Accounting upserts on (vm_id, period_start), which needs a unique index
-- without a WHERE clause. A database built from the fresh migration chain only
-- has 004's partial one, so every upsert failed there. Added only when no such
-- index exists and no duplicates stand in the way; otherwise left as it is.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index i JOIN pg_class t ON t.oid = i.indrelid
        WHERE t.relname = 'bandwidth_usages' AND i.indisunique AND i.indpred IS NULL
            AND (SELECT array_agg(a.attname::text ORDER BY k.ord)
                 FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
                 JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum) = ARRAY['vm_id', 'period_start']
    ) AND NOT EXISTS (
        SELECT 1 FROM bandwidth_usages GROUP BY vm_id, period_start HAVING count(*) > 1
    ) THEN
        CREATE UNIQUE INDEX idx_bw_vm_period_all ON bandwidth_usages (vm_id, period_start);
    END IF;
END $$;

-- The day of the month a plan's transfer allowance starts again, and which
-- direction counts toward it: both, out (sent by the VM) or in. The defaults
-- keep today's calendar month counting both ways.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS transfer_reset_day SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS transfer_counts VARCHAR(8) NOT NULL DEFAULT 'both';

-- Per account: a reset day that overrides the plans' (NULL keeps theirs), and
-- pooled transfer, where the account's VMs share the sum of their allowances.
ALTER TABLE users ADD COLUMN IF NOT EXISTS transfer_reset_day SMALLINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS transfer_pooled BOOLEAN NOT NULL DEFAULT FALSE;

-- Graph history: daily averages, kept two years, rolled up from the hourly ones.
CREATE TABLE IF NOT EXISTS vm_metrics_daily (
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
CREATE INDEX IF NOT EXISTS idx_vm_metrics_daily_bucket ON vm_metrics_daily (bucket);

-- Reverse DNS: the zone a pool's PTR records are written to when it is not the
-- standard one, such as an RFC 2317 classless zone for a block smaller than a
-- /24 ("0-127.113.0.203.in-addr.arpa"). Empty keeps the standard zone.
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS rdns_zone VARCHAR(255) NOT NULL DEFAULT '';

-- PTR records a VM's owner sets for addresses inside the IPv6 prefix routed to
-- the VM, which are not rows in ip_addresses.
CREATE TABLE IF NOT EXISTS vm_prefix_ptrs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id TEXT NOT NULL,
    address INET NOT NULL,
    rdns VARCHAR(253) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT vm_prefix_ptrs_address_key UNIQUE (address)
);
CREATE INDEX IF NOT EXISTS idx_vm_prefix_ptrs_vm ON vm_prefix_ptrs (vm_id);

-- Abuse: inbound connection and packet rates per guest, next to the outbound
-- connection rate already kept.
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS syn_in_total BIGINT NOT NULL DEFAULT 0;
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS syn_in_rate DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS packets_out_total BIGINT NOT NULL DEFAULT 0;
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS packets_in_total BIGINT NOT NULL DEFAULT 0;
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS pps_out DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE guest_connections ADD COLUMN IF NOT EXISTS pps_in DOUBLE PRECISION NOT NULL DEFAULT 0;

-- One row per episode: a guest over a threshold, from the first sample over it
-- to the first one back under, with what was done about it and the readings
-- that show why. A manual null-route or quarantine is an episode too.
CREATE TABLE IF NOT EXISTS abuse_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id TEXT NOT NULL,
    mac VARCHAR(17) NOT NULL DEFAULT '',
    vm_id TEXT NOT NULL DEFAULT '',
    vm_hostname VARCHAR(255) NOT NULL DEFAULT '',
    kind VARCHAR(24) NOT NULL,
    metric DOUBLE PRECISION NOT NULL DEFAULT 0,
    peak DOUBLE PRECISION NOT NULL DEFAULT 0,
    threshold DOUBLE PRECISION NOT NULL DEFAULT 0,
    samples INTEGER NOT NULL DEFAULT 0,
    action VARCHAR(16) NOT NULL DEFAULT 'alert',
    action_state VARCHAR(16) NOT NULL DEFAULT 'none',
    targets TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    evidence JSONB NOT NULL DEFAULT '[]',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    released_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_abuse_events_started ON abuse_events (started_at DESC);
CREATE INDEX IF NOT EXISTS idx_abuse_events_open ON abuse_events (node_id, mac, kind) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_abuse_events_action ON abuse_events (action_state) WHERE action_state = 'active';
