-- Tenant-owned VPCs.
--
-- A VPC is a managed_network of type 'vpc'. user_id marks the tenant that owns
-- it; NULL means an administrator-owned network (every pre-existing row), so no
-- live data needs migrating.
--
-- Subnets are deliberately NOT globally unique: two tenants may both use
-- 10.0.0.0/24, because each VPC's gateway lives in its own router namespace on
-- the node. Overlap is only rejected WITHIN one tenant, where it would be
-- ambiguous (a customer cannot hold both 10.0.0.0/24 and 10.0.0.0/23). That
-- check needs CIDR containment in both directions, which no unique index can
-- express, so it is enforced in the service under a per-user advisory lock.
ALTER TABLE managed_networks
    ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_managed_networks_user ON managed_networks (user_id);
CREATE INDEX IF NOT EXISTS idx_managed_networks_vpc
    ON managed_networks (type) WHERE type = 'vpc';
