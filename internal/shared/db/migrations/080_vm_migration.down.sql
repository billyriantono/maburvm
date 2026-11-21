DROP TABLE IF EXISTS vm_migrations;
DROP TABLE IF EXISTS node_drains;
ALTER TABLE nodes DROP COLUMN IF EXISTS migration_identity_at;
ALTER TABLE nodes DROP COLUMN IF EXISTS ssh_host_keys;
ALTER TABLE nodes DROP COLUMN IF EXISTS migration_public_key;
