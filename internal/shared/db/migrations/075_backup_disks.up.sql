-- Every disk of a backup, primary first ([] or NULL = the primary disk only,
-- as every backup made before this), and how its copy was taken ('' = unknown,
-- read while the VM ran). Nullable: the panel writes an empty list as NULL.
ALTER TABLE backups ADD COLUMN IF NOT EXISTS disks JSONB DEFAULT '[]';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS consistency VARCHAR(20) NOT NULL DEFAULT '';

-- The scheduler now finds due schedules by next_run_at; a database built from
-- the older migrations may lack the index.
CREATE INDEX IF NOT EXISTS idx_backup_schedules_next_run_at ON backup_schedules(next_run_at);
