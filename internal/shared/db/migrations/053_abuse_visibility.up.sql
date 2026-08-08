-- Make node exhaustion and guest abuse visible in the panel.
--
-- Both come from the same incident: a node was found with its conntrack table
-- pinned at 100% while CPU, memory and bandwidth all looked healthy, so nothing
-- in the panel showed a problem — the only symptom was tenants intermittently
-- losing connectivity. The cause was two guests running an outbound port scan,
-- and neither of them was in the panel's own VM list.

-- Connection tracking is a hard ceiling: once full, the node refuses NEW
-- connections for every tenant on it. Recorded alongside the other node metrics
-- so it appears on the same history as CPU and memory.
ALTER TABLE node_metrics
    ADD COLUMN IF NOT EXISTS conntrack_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS conntrack_max   BIGINT NOT NULL DEFAULT 0;

-- One row per guest NIC seen on a node, keyed on MAC rather than VM id.
--
-- MAC because the guests that matter here are often ones the panel does not
-- manage (so there is no VM id), and because an abusive guest may be running a
-- spoofed or duplicated address — two live guests were found sharing one.
CREATE TABLE IF NOT EXISTS guest_connections (
    id               BIGSERIAL PRIMARY KEY,
    node_id          UUID        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    mac              VARCHAR(17) NOT NULL,

    -- Empty when the domain is not one the panel manages; that is expected, not
    -- an error, and is precisely the case worth surfacing.
    vm_id            VARCHAR(64) NOT NULL DEFAULT '',
    vm_hostname      VARCHAR(255) NOT NULL DEFAULT '',
    interface_name   VARCHAR(32) NOT NULL DEFAULT '',

    -- syn_total is the cumulative counter last read from the node. It is kept so
    -- the next sample can be differenced against it; a counter that goes
    -- backwards means the node's rules were rebuilt and that sample is skipped
    -- rather than reported as a negative rate.
    syn_total        BIGINT      NOT NULL DEFAULT 0,
    syn_rate         DOUBLE PRECISION NOT NULL DEFAULT 0,
    peak_rate        DOUBLE PRECISION NOT NULL DEFAULT 0,

    quarantined      BOOLEAN     NOT NULL DEFAULT FALSE,
    quarantine_reason TEXT       NOT NULL DEFAULT '',

    -- first_flagged_at is set the first time the guest exceeds the threshold and
    -- cleared when it settles, so "how long has this been going on" is answerable
    -- without keeping a full time series of every quiet guest on every node.
    first_flagged_at TIMESTAMPTZ,
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT guest_connections_node_mac_key UNIQUE (node_id, mac)
);

-- The admin view is "show me the worst offenders across all nodes".
CREATE INDEX IF NOT EXISTS idx_guest_connections_rate ON guest_connections (syn_rate DESC);
CREATE INDEX IF NOT EXISTS idx_guest_connections_flagged ON guest_connections (first_flagged_at) WHERE first_flagged_at IS NOT NULL;
