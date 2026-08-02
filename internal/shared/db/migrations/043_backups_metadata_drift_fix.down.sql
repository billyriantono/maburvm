-- Reverse the backups metadata drift fix. The enum type is left in place (other
-- objects may reference it); only the columns this migration added are dropped.
ALTER TABLE backups DROP COLUMN IF EXISTS completed_at;
ALTER TABLE backups DROP COLUMN IF EXISTS started_at;
ALTER TABLE backups DROP COLUMN IF EXISTS error_message;
ALTER TABLE backups DROP COLUMN IF EXISTS checksum;
ALTER TABLE backups DROP COLUMN IF EXISTS compression;
ALTER TABLE backups DROP COLUMN IF EXISTS backup_type;
ALTER TABLE backups DROP COLUMN IF EXISTS storage_path;
