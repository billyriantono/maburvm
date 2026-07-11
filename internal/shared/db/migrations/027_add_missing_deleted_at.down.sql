ALTER TABLE vms            DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE nodes          DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE os_templates   DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE firewall_rules DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE snapshots      DROP COLUMN IF EXISTS deleted_at;
