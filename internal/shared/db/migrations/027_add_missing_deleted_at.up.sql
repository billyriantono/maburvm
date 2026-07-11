-- Add the soft-delete column GORM expects on tables whose models embed
-- gorm.DeletedAt but whose CREATE TABLE migrations never defined `deleted_at`.
-- On a FRESH database (built purely from these migrations) GORM's automatic
-- "WHERE deleted_at IS NULL" clause referenced a non-existent column, so listing
-- VMs, nodes, templates, firewall rules, and snapshots failed with a 500.
--
-- Older/existing databases were built from a previous migration set that already
-- had these columns; ADD COLUMN IF NOT EXISTS makes this a safe no-op there.
ALTER TABLE vms            ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE nodes          ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE os_templates   ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE firewall_rules ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE snapshots      ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_vms_deleted_at            ON vms(deleted_at);
CREATE INDEX IF NOT EXISTS idx_nodes_deleted_at          ON nodes(deleted_at);
CREATE INDEX IF NOT EXISTS idx_os_templates_deleted_at   ON os_templates(deleted_at);
CREATE INDEX IF NOT EXISTS idx_firewall_rules_deleted_at ON firewall_rules(deleted_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_deleted_at      ON snapshots(deleted_at);
