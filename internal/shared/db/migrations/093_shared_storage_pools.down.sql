ALTER TABLE IF EXISTS vm_migrations DROP COLUMN IF EXISTS kept_paths;
DROP INDEX IF EXISTS idx_storage_pools_shared_pool;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS shared_pool_id;
DROP TABLE IF EXISTS shared_storage_pools;
