ALTER TABLE backup_schedules DROP COLUMN IF EXISTS last_backup_id;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS last_run_at;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS next_run_at;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS retention_policy;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS compression;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS storage_provider;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS schedule;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS status;
