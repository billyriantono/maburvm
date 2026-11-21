-- Fixtures for the browser tests: three accounts, one node (the fake agent on
-- 127.0.0.1), an address pool, a template, a plan, and one customer VM that
-- the fake agent is started holding (stack.sh passes its id). Applied to a
-- throwaway database after the panel has migrated it. Passwords are in
-- e2e/fixtures.ts.

-- The founding administrator (the Owner) is the earliest-created one.
INSERT INTO users (id, email, name, password_hash, role, created_at) VALUES
  ('e2e00000-0000-0000-0000-000000000001', 'owner@e2e.test', 'Olivia Owner',
   '$2a$10$Ir00Hg8Zz6n0/5vB8Q6WmedrIsCjO/mIaPqiqQoRJPeoway8pGzuy', 'admin', now() - interval '1 day'),
  ('e2e00000-0000-0000-0000-000000000002', 'customer@e2e.test', 'Carl Customer',
   '$2a$10$eokLeevLXaDnYMlT/cLRwe2LwJfGsK5x5FroxzpsPSAX2PLtJIlRS', 'client', now()),
  ('e2e00000-0000-0000-0000-000000000004', 'locked@e2e.test', 'Lou Locked',
   '$2a$10$eokLeevLXaDnYMlT/cLRwe2LwJfGsK5x5FroxzpsPSAX2PLtJIlRS', 'client', now());
INSERT INTO users (id, email, name, password_hash, role, staff_role) VALUES
  ('e2e00000-0000-0000-0000-000000000003', 'support@e2e.test', 'Sam Support',
   '$2a$10$8ZigjHeFxUqjLVn8DD6Ufeu3Mjlf1YTyPcAqhU3zLL2vmZOteR/wW', 'admin', 'support');

-- Three wrong passwords lock an account, so the lockout test stays within the
-- sign-in rate limit.
INSERT INTO system_settings (section, data) VALUES ('security', '{"lockoutAttempts": 3, "lockoutMinutes": 15}')
  ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data;

INSERT INTO nodes (id, name, ip_address, status, token, cpu_count, memory_total_bytes) VALUES
  ('e2e00000-0000-0000-0000-0000000000a1', 'e2e-node', '127.0.0.1', 'active', 'e2e-node-token', 16, 68719476736);

INSERT INTO storage_pools (id, name, type, status, total_space, available_space, path, node_id, is_primary) VALUES
  ('e2e00000-0000-0000-0000-0000000000a2', 'vm', 'dir', 'online', 2199023255552, 2000000000000, '/vm',
   'e2e00000-0000-0000-0000-0000000000a1', true);

INSERT INTO ip_pools (id, name, node_id, family, cidr, gateway, range_start, range_end, bridge, orderable) VALUES
  ('e2e00000-0000-0000-0000-0000000000b1', 'e2e-public', 'e2e00000-0000-0000-0000-0000000000a1', 'ipv4',
   '192.0.2.0/24', '192.0.2.1', '192.0.2.10', '192.0.2.40', 'br0', true);
INSERT INTO ip_pool_nodes (pool_id, node_id) VALUES
  ('e2e00000-0000-0000-0000-0000000000b1', 'e2e00000-0000-0000-0000-0000000000a1');
INSERT INTO ip_addresses (pool_id, node_id, address, status)
  SELECT 'e2e00000-0000-0000-0000-0000000000b1', 'e2e00000-0000-0000-0000-0000000000a1', ('192.0.2.' || n)::inet, 'available'
  FROM generate_series(11, 40) AS n;

INSERT INTO os_templates (id, name, version, image_path, description, size_bytes) VALUES
  ('e2e00000-0000-0000-0000-0000000000c1', 'Debian', '12', '/var/lib/maburvm/templates/debian-12.qcow2', 'Debian 12 (Bookworm)', 1073741824);

INSERT INTO plans (id, name, cpu, ram, disk, bandwidth_mbps, description, price_monthly, currency) VALUES
  ('e2e00000-0000-0000-0000-0000000000d1', 'E2E Starter', 1, 1024, 20, 100, 'One core, 1 GB', 5, 'USD');

-- The customer's server, with its address and a rule letting SSH in.
INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, plan_id, resources, status) VALUES
  ('e2e00000-0000-0000-0000-0000000000e1', 'e2e00000-0000-0000-0000-000000000002', 'e2e00000-0000-0000-0000-0000000000a1',
   'shop-e2e', 'e2e00000-0000-0000-0000-0000000000c1', 'e2e00000-0000-0000-0000-0000000000d1',
   '{"cpu":1,"ram":1024,"disk":20}', 'running');
UPDATE ip_addresses SET status = 'assigned', vm_id = 'e2e00000-0000-0000-0000-0000000000e1', user_id = 'e2e00000-0000-0000-0000-000000000002'
  WHERE address = '192.0.2.11';
INSERT INTO networks (vm_id, ip_address, bandwidth_limit) VALUES ('e2e00000-0000-0000-0000-0000000000e1', '192.0.2.11', 100);
