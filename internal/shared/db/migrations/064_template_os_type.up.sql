-- What kind of system a template holds. It decides how a VM built from it is
-- set up: Linux through cloud-init, RouterOS through a script on its disk.
ALTER TABLE os_templates ADD COLUMN IF NOT EXISTS os_type VARCHAR(20) NOT NULL DEFAULT 'linux';

-- The panel stopped recording a checksum when templates became a URL the node
-- downloads, but a database built from the first migration still requires one,
-- so adding a template there failed. Older databases already allow NULL.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'os_templates' AND column_name = 'checksum' AND is_nullable = 'NO') THEN
        ALTER TABLE os_templates ALTER COLUMN checksum DROP NOT NULL;
    END IF;
END $$;
