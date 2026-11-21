-- Network cards, DHCP and resolvers per pool, VLAN bridges per node, and
-- further IPv6 addresses inside a routed prefix. Ids are kept as text: on
-- older databases some id columns are varchar rather than uuid.

-- The resolvers a pool's guests are given (comma-separated; empty keeps the
-- built-in ones), and whether the node answers DHCP for the pool's assigned
-- addresses. Both default to what every pool did before.
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS nameservers TEXT NOT NULL DEFAULT '';
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS dhcp BOOLEAN NOT NULL DEFAULT false;

-- A VM's network cards, with the MAC each one has. Position 0 is the card the
-- VM was created with. VMs created before this have no rows until their
-- cards are read from their node.
CREATE TABLE IF NOT EXISTS vm_nics (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id TEXT NOT NULL,
    position SMALLINT NOT NULL,
    bridge VARCHAR(64) NOT NULL DEFAULT '',
    mac_address VARCHAR(17) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_nics_vm_position ON vm_nics (vm_id, position);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_nics_mac ON vm_nics (lower(mac_address));

-- Addresses inside a VM's routed IPv6 prefix that the guest holds besides its
-- ::1 (comma-separated).
ALTER TABLE vm_ipv6_prefixes ADD COLUMN IF NOT EXISTS extra_addresses TEXT NOT NULL DEFAULT '';

-- Bridges of a node that carry per-VM VLANs, as "bridge:uplink,...". NULL
-- leaves it to the agent's own environment, which is how it worked before.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS vlan_bridges TEXT;
