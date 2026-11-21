-- Firewall groups: named rule sets attached to many VMs. user_id NULL is a
-- group staff manage for everyone; otherwise the customer who owns it.
CREATE TABLE IF NOT EXISTS firewall_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID,
    name VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_firewall_groups_user_id ON firewall_groups (user_id);

-- The same rule fields as firewall_rules.
CREATE TABLE IF NOT EXISTS firewall_group_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id UUID NOT NULL REFERENCES firewall_groups(id) ON DELETE CASCADE,
    protocol VARCHAR(10) NOT NULL,
    port_range VARCHAR(50) NOT NULL DEFAULT '',
    action VARCHAR(10) NOT NULL,
    direction VARCHAR(10) NOT NULL,
    source_ip CIDR DEFAULT '0.0.0.0/0',
    priority INTEGER NOT NULL DEFAULT 100,
    icmp_type VARCHAR(32) NOT NULL DEFAULT '',
    log BOOLEAN NOT NULL DEFAULT FALSE,
    family VARCHAR(4) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_firewall_group_rules_group_id ON firewall_group_rules (group_id);

-- position: 'before' the VM's own rules (they cannot override it) or 'after'
-- (they can). sort_order orders groups on the same side.
CREATE TABLE IF NOT EXISTS vm_firewall_groups (
    vm_id UUID NOT NULL,
    group_id UUID NOT NULL REFERENCES firewall_groups(id) ON DELETE CASCADE,
    position VARCHAR(6) NOT NULL DEFAULT 'after',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (vm_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_vm_firewall_groups_group_id ON vm_firewall_groups (group_id);

-- A plan's default group replaces the built-in default rules for VMs created
-- on it. NULL keeps the built-in set. network_type 'nat' plans give each VM a
-- private address and a block of nat_ports public ports on its node.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS default_firewall_group_id UUID;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS network_type VARCHAR(8) NOT NULL DEFAULT 'public';
ALTER TABLE plans ADD COLUMN IF NOT EXISTS nat_ports INTEGER NOT NULL DEFAULT 0;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS nat_hostnames INTEGER NOT NULL DEFAULT 0;

-- A NAT pool holds private addresses masqueraded behind the node.
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS nat BOOLEAN NOT NULL DEFAULT FALSE;

-- The node's public ports NAT VMs take their blocks from, and the address the
-- node publishes them on, as its agent last reported. None (0/0) until staff
-- choose a range: only they know what else the node listens on.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS nat_port_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS nat_port_end INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS nat_address VARCHAR(64) NOT NULL DEFAULT '';

-- One contiguous block of public ports per NAT VM, on its node. Overlap is
-- prevented by the panel under a per-node lock.
CREATE TABLE IF NOT EXISTS nat_port_blocks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL,
    node_id UUID NOT NULL,
    start_port INTEGER NOT NULL,
    port_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_nat_port_blocks_vm ON nat_port_blocks (vm_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_nat_port_blocks_node_start ON nat_port_blocks (node_id, start_port);

-- Hostnames a NAT VM's owner routes to it through the node's web ports. Only
-- a verified hostname is routed, and one hostname is verified once.
CREATE TABLE IF NOT EXISTS vm_hostnames (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL,
    hostname VARCHAR(253) NOT NULL,
    http_port INTEGER NOT NULL DEFAULT 80,
    https_port INTEGER NOT NULL DEFAULT 443,
    verify_token VARCHAR(64) NOT NULL,
    verified_at TIMESTAMPTZ,
    node_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vm_hostnames_vm_id ON vm_hostnames (vm_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_vm_hostnames_vm_hostname ON vm_hostnames (vm_id, hostname);
CREATE UNIQUE INDEX IF NOT EXISTS ux_vm_hostnames_verified ON vm_hostnames (hostname) WHERE verified_at IS NOT NULL;
