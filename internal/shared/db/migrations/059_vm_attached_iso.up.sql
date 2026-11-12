-- The ISO in a VM's CD drive, as last confirmed by its node. NULL = not known
-- (every VM that existed before this column), '' = none, otherwise the image.
-- Deliberately nullable with no default: existing VMs may have an ISO attached
-- that the panel never recorded, and "unknown" must not read as "none".
ALTER TABLE vms ADD COLUMN IF NOT EXISTS attached_iso TEXT;
