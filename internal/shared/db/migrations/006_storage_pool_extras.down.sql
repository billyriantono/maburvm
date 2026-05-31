ALTER TABLE storage_pools DROP CONSTRAINT IF EXISTS chk_storage_pool_alert_threshold;
ALTER TABLE storage_pools DROP CONSTRAINT IF EXISTS chk_storage_pool_file_format;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS is_primary;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS overcommit;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS alert_threshold;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS file_format;
