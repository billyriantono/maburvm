-- Forward drift fix for the `backups` table.
--
-- Migration 004 already declared these columns via ADD COLUMN IF NOT EXISTS, but
-- on databases built from an older set they never materialized: the ALTERs
-- reference the `backup_type` enum, which was not present, so the block did not
-- take effect while 004 was still recorded as applied. The result is a minimal
-- backups table (id, vm_id, storage_provider, status, size, timestamps) missing
-- the metadata the model and the restore path depend on — notably `checksum`
-- (restore refuses a backup with no recorded checksum) and `storage_path`
-- (the object key the agent downloads). This migration is idempotent: it creates
-- the enum only if absent and each column with IF NOT EXISTS, so it is safe on
-- both drifted and already-correct databases.

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'backup_type') THEN
    CREATE TYPE backup_type AS ENUM ('manual', 'scheduled');
  END IF;
END $$;

ALTER TABLE backups ADD COLUMN IF NOT EXISTS storage_path VARCHAR(500) NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS backup_type backup_type NOT NULL DEFAULT 'manual';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS compression VARCHAR(20) DEFAULT 'gzip';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS checksum VARCHAR(64);
ALTER TABLE backups ADD COLUMN IF NOT EXISTS error_message TEXT;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS started_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP WITH TIME ZONE;

CREATE INDEX IF NOT EXISTS idx_backups_vm_id ON backups(vm_id);
