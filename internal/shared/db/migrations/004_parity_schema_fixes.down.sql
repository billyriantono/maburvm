-- Rollback parity schema fixes.
-- This intentionally leaves enum types if other objects still depend on them.

DROP TABLE IF EXISTS bandwidth_usages;
DROP TABLE IF EXISTS backup_schedules;
DROP TABLE IF EXISTS port_forwards;
DROP TABLE IF EXISTS storage_volumes;
DROP TABLE IF EXISTS storage_pools;

DROP INDEX IF EXISTS idx_networks_vm_id;
DROP INDEX IF EXISTS idx_networks_deleted_at;
ALTER TABLE networks DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE networks DROP COLUMN IF EXISTS bandwidth_quota_gb;

DROP INDEX IF EXISTS idx_backups_vm_id;
DROP INDEX IF EXISTS idx_backups_deleted_at;
ALTER TABLE backups DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE backups DROP COLUMN IF EXISTS completed_at;
ALTER TABLE backups DROP COLUMN IF EXISTS started_at;
ALTER TABLE backups DROP COLUMN IF EXISTS error_message;
ALTER TABLE backups DROP COLUMN IF EXISTS checksum;
ALTER TABLE backups DROP COLUMN IF EXISTS compression;
ALTER TABLE backups DROP COLUMN IF EXISTS backup_type;
ALTER TABLE backups DROP COLUMN IF EXISTS storage_path;

DROP INDEX IF EXISTS idx_users_deleted_at;
ALTER TABLE users DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE users DROP COLUMN IF EXISTS two_factor_backup_codes;

DROP TYPE IF EXISTS backup_schedule_status;
DROP TYPE IF EXISTS backup_type;
