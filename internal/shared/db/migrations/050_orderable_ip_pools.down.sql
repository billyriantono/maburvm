DROP INDEX IF EXISTS idx_ip_pools_orderable;
ALTER TABLE ip_pools DROP COLUMN IF EXISTS orderable;
