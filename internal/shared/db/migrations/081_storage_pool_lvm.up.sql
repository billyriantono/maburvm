-- LVM pools: why a pool cannot hold disks (a missing volume group, one that
-- holds the host's own system), and a thin pool's metadata use, which fills
-- independently of its data and stops every thin volume when it does.
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS status_note TEXT NOT NULL DEFAULT '';
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS metadata_percent DOUBLE PRECISION;
