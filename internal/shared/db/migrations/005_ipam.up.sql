CREATE TABLE IF NOT EXISTS ip_pools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    node_id UUID REFERENCES nodes(id) ON DELETE SET NULL,
    family VARCHAR(8) NOT NULL DEFAULT 'ipv4' CHECK (family IN ('ipv4', 'ipv6')),
    cidr CIDR,
    gateway INET,
    range_start INET,
    range_end INET,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS ip_addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pool_id UUID NOT NULL REFERENCES ip_pools(id) ON DELETE CASCADE,
    node_id UUID REFERENCES nodes(id) ON DELETE SET NULL,
    address INET NOT NULL,
    family VARCHAR(8) NOT NULL DEFAULT 'ipv4' CHECK (family IN ('ipv4', 'ipv6')),
    status VARCHAR(16) NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'reserved', 'assigned', 'disabled')),
    vm_id UUID REFERENCES vms(id) ON DELETE SET NULL,
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ip_pools_node_id ON ip_pools(node_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ip_addresses_pool_status ON ip_addresses(pool_id, status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ip_addresses_node_id ON ip_addresses(node_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ip_addresses_vm_id ON ip_addresses(vm_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ip_addresses_pool_address_unique ON ip_addresses(pool_id, address) WHERE deleted_at IS NULL;
