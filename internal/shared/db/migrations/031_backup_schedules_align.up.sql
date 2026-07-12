-- Align backup_schedules with the BackupSchedule model. The live DB was built
-- from an older migration set and drifted to an enabled/frequency shape, so the
-- scheduler's `WHERE status = 'active'` load failed with "column status does not
-- exist". Additive forward-only ALTERs (IF NOT EXISTS) — the table is empty so no
-- backfill is needed; legacy columns are left in place. See db-schema-drift.
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS status           VARCHAR(20)  NOT NULL DEFAULT 'active';
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS schedule         VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS storage_provider VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS compression      VARCHAR(20)  NOT NULL DEFAULT 'gzip';
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS retention_policy JSONB        NOT NULL DEFAULT '{}';
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS next_run_at      TIMESTAMPTZ;
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS last_run_at      TIMESTAMPTZ;
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS last_backup_id   UUID;
