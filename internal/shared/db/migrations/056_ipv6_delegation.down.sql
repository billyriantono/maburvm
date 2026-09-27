DROP TABLE IF EXISTS vm_ipv6_prefixes;
ALTER TABLE ip_pools DROP COLUMN IF EXISTS delegated_prefix_len;
