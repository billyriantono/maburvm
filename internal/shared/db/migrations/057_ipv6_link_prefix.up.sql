-- The router<->node link network of a delegating IPv6 pool when it lies inside
-- the pool (e.g. 2001:db8:21:104::/64 of 2001:db8:21::/48). It is never
-- delegated to a VM. NULL: the link network is the pool's first prefix (idx 0),
-- which is never delegated anyway.
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS link_prefix CIDR;
