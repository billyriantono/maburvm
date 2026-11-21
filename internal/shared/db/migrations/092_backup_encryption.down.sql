-- Encrypted backups cannot be restored once their keys are gone: only go down
-- after deleting them, or with the keys exported.
ALTER TABLE backups DROP COLUMN IF EXISTS encryption_key_id;
ALTER TABLE backup_destinations DROP COLUMN IF EXISTS encryption_key_id;
ALTER TABLE backup_destinations DROP COLUMN IF EXISTS encryption_keys;
ALTER TABLE backup_destinations DROP COLUMN IF EXISTS encrypt;
