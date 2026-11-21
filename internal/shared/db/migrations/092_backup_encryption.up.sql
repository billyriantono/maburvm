-- Client-side backup encryption. A destination keeps its keys, each sealed
-- with the panel's AES key; encryption_key_id names the one new backups are
-- encrypted with while encrypt is on. Older keys stay so the backups made with
-- them still restore. A backup records the key it was encrypted with; ''
-- means it was stored in the clear, as every backup made before this was.
ALTER TABLE backup_destinations ADD COLUMN IF NOT EXISTS encrypt BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE backup_destinations ADD COLUMN IF NOT EXISTS encryption_keys JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE backup_destinations ADD COLUMN IF NOT EXISTS encryption_key_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS encryption_key_id VARCHAR(36) NOT NULL DEFAULT '';
