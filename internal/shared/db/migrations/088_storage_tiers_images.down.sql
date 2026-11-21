DROP TABLE IF EXISTS vm_disk_moves;
ALTER TABLE vms DROP COLUMN IF EXISTS attached_iso_2;
DROP TABLE IF EXISTS template_node_status;
ALTER TABLE os_templates DROP CONSTRAINT IF EXISTS os_templates_firmware_check;
ALTER TABLE os_templates DROP CONSTRAINT IF EXISTS os_templates_kind_check;
ALTER TABLE os_templates
    DROP COLUMN IF EXISTS firmware,
    DROP COLUMN IF EXISTS auto_refresh,
    DROP COLUMN IF EXISTS allow_http,
    DROP COLUMN IF EXISTS checksum_checked_at,
    DROP COLUMN IF EXISTS checksum_url,
    DROP COLUMN IF EXISTS checksum_sha256,
    DROP COLUMN IF EXISTS kind;
ALTER TABLE plans DROP COLUMN IF EXISTS storage_tier;
ALTER TABLE storage_pools DROP COLUMN IF EXISTS tier;
