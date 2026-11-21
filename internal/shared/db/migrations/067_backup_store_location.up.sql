-- Where a backup's file is kept is now recorded in backups.storage_provider:
-- 's3' for the destination set in the panel, 'node' for storage only the VM's
-- node knows (the keys in its environment file).
--
-- Every backup that exists before this migration was written through a node's
-- own keys: the panel had no destination to give. Marking them 'node' keeps a
-- restore of one going to the node's storage, where the file is, and stops a
-- delete from claiming to have removed a file the panel never looked at.
ALTER TABLE backups ADD COLUMN IF NOT EXISTS storage_provider VARCHAR(100) NOT NULL DEFAULT 's3';
UPDATE backups SET storage_provider = 'node' WHERE storage_provider IS DISTINCT FROM 'node';
