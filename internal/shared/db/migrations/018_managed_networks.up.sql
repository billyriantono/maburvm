-- Administrator-defined virtual networks (bridge/NAT/isolated) — the managed
-- "Network" concept, distinct from per-VM IP records and IP pools.
CREATE TABLE IF NOT EXISTS managed_networks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       VARCHAR(100) NOT NULL,
    type       VARCHAR(20) NOT NULL DEFAULT 'bridge',
    bridge     VARCHAR(50),
    subnet     VARCHAR(64),
    gateway    VARCHAR(64),
    dhcp_start VARCHAR(64),
    dhcp_end   VARCHAR(64),
    vlan_id    INTEGER NOT NULL DEFAULT 0,
    node_id    UUID REFERENCES nodes(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_managed_networks_node ON managed_networks(node_id) WHERE deleted_at IS NULL;
