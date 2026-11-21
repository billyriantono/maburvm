-- Storage tiers: a pool's tier, and the tier a plan puts its VMs' disks on.
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS tier varchar(32) NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN IF NOT EXISTS storage_tier varchar(32) NOT NULL DEFAULT '';

-- Images: disk images and ISOs told apart, a checksum every download is held
-- to (and where to read it again), plain http only by choice, boot firmware.
ALTER TABLE os_templates
    ADD COLUMN IF NOT EXISTS kind varchar(8) NOT NULL DEFAULT 'disk',
    ADD COLUMN IF NOT EXISTS checksum_sha256 varchar(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS checksum_url text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS checksum_checked_at timestamptz,
    ADD COLUMN IF NOT EXISTS allow_http boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS auto_refresh boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS firmware varchar(8) NOT NULL DEFAULT 'bios';

-- The ISOs page has always added .iso files as templates.
UPDATE os_templates SET kind = 'iso'
WHERE kind = 'disk' AND lower(image_path) ~ '\.iso([?#].*)?$';

-- Templates already on plain http keep working; new ones have to opt in.
UPDATE os_templates SET allow_http = true WHERE image_path ILIKE 'http://%';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'os_templates_kind_check') THEN
        ALTER TABLE os_templates ADD CONSTRAINT os_templates_kind_check CHECK (kind IN ('disk', 'iso'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'os_templates_firmware_check') THEN
        ALTER TABLE os_templates ADD CONSTRAINT os_templates_firmware_check CHECK (firmware IN ('bios', 'uefi'));
    END IF;
END $$;

-- Where each node stands with each image. The template sync has written here
-- all along without the table existing.
CREATE TABLE IF NOT EXISTS template_node_status (
    template_id uuid NOT NULL REFERENCES os_templates(id) ON DELETE CASCADE,
    node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    status varchar(20) NOT NULL DEFAULT 'pending',
    error_msg text,
    synced_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (template_id, node_id)
);
-- The checksum of the copy the node holds.
ALTER TABLE template_node_status ADD COLUMN IF NOT EXISTS checksum varchar(64) NOT NULL DEFAULT '';

-- A second CD drive, for a drivers disc beside an installer.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS attached_iso_2 text;

-- A disk moving between two pools of its node.
CREATE TABLE IF NOT EXISTS vm_disk_moves (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id uuid NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    node_id uuid NOT NULL,
    target varchar(16) NOT NULL,
    size_gb integer NOT NULL DEFAULT 0,
    source_pool_id uuid,
    dest_pool_id uuid NOT NULL,
    source_path text NOT NULL DEFAULT '',
    dest_path text NOT NULL DEFAULT '',
    live boolean NOT NULL DEFAULT false,
    status varchar(16) NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    phase text NOT NULL DEFAULT '',
    total_bytes bigint NOT NULL DEFAULT 0,
    done_bytes bigint NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    cancel_requested boolean NOT NULL DEFAULT false,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_vm_disk_moves_vm ON vm_disk_moves (vm_id, created_at DESC);
-- One move of a VM's disks at a time.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_vm_disk_moves_active ON vm_disk_moves (vm_id)
    WHERE status IN ('queued', 'running');
