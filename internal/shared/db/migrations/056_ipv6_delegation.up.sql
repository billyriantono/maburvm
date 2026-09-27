-- Routed IPv6: a pool may delegate one prefix (normally a /64) per VM out of
-- its CIDR instead of handing out single addresses. Prefixes are computed
-- arithmetically from an index, never pre-generated as rows.
ALTER TABLE ip_pools
    ADD COLUMN IF NOT EXISTS delegated_prefix_len SMALLINT;

-- One delegated prefix per VM. idx 0 is the pool's own first subnet (the
-- router<->node link network) and is never delegated, so idx starts at 1.
CREATE TABLE IF NOT EXISTS vm_ipv6_prefixes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pool_id UUID NOT NULL REFERENCES ip_pools(id) ON DELETE CASCADE,
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    prefix CIDR NOT NULL UNIQUE,
    idx INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vm_id),
    UNIQUE (pool_id, idx)
);
