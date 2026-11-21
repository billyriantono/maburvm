-- Backup destinations: S3, SFTP, or a directory mounted on the nodes. Each
-- backup records the one holding it, so changing where new backups go keeps
-- older ones restorable and deletable. Credentials are in secret_enc,
-- encrypted with the panel's key; config holds nothing secret.
CREATE TABLE IF NOT EXISTS backup_destinations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    kind VARCHAR(10) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    secret_enc TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_backup_destinations_default ON backup_destinations (is_default) WHERE is_default;

-- The S3 destination saved in system settings until now becomes the default.
-- Its row is left in place for a panel rolled back to the previous version.
INSERT INTO backup_destinations (name, kind, config, secret_enc, is_default)
SELECT 'S3 bucket', 's3', (data::jsonb - 'secret_key_enc'), COALESCE(data::jsonb->>'secret_key_enc', ''), TRUE
FROM system_settings
WHERE section = 'backup_destination'
  AND COALESCE(data::jsonb->>'bucket', '') <> ''
  AND NOT EXISTS (SELECT 1 FROM backup_destinations);

-- Where each backup is and what it builds on. mode is 'full' or
-- 'incremental'; an incremental's parent_id is the backup under it, and
-- chain_id is the full backup the chain starts from. checkpoint is the libvirt
-- checkpoint the backup created on its VM; node_id the node that wrote it.
ALTER TABLE backups ADD COLUMN IF NOT EXISTS destination_id UUID;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS node_id UUID;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS mode VARCHAR(12) NOT NULL DEFAULT 'full';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS parent_id UUID;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS chain_id UUID;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS checkpoint VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS mode_note TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_backups_parent_id ON backups (parent_id);
CREATE INDEX IF NOT EXISTS idx_backups_destination_id ON backups (destination_id);

UPDATE backups SET destination_id = (SELECT id FROM backup_destinations WHERE is_default)
WHERE storage_provider = 's3' AND destination_id IS NULL;

-- full_every_days > 0 makes scheduled backups incremental, with a full one at
-- least that often; 0 keeps every scheduled backup full, as before.
-- chain_reset_at: the VM's disks were replaced then, so no backup from before
-- may be built on.
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS full_every_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backup_schedules ADD COLUMN IF NOT EXISTS chain_reset_at TIMESTAMPTZ;
