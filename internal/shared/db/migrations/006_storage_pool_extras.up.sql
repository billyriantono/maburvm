-- Add extra fields to storage_pools
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS file_format VARCHAR(20) NOT NULL DEFAULT 'raw';
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS alert_threshold INTEGER NOT NULL DEFAULT 90;
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS overcommit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS is_primary BOOLEAN NOT NULL DEFAULT false;

-- Constraint: file_format must be raw or qcow2
ALTER TABLE storage_pools DROP CONSTRAINT IF EXISTS chk_storage_pool_file_format;
ALTER TABLE storage_pools ADD CONSTRAINT chk_storage_pool_file_format CHECK (file_format IN ('raw', 'qcow2'));

-- Constraint: alert_threshold 0-100
ALTER TABLE storage_pools DROP CONSTRAINT IF EXISTS chk_storage_pool_alert_threshold;
ALTER TABLE storage_pools ADD CONSTRAINT chk_storage_pool_alert_threshold CHECK (alert_threshold >= 0 AND alert_threshold <= 100);
