-- Suspension as a fact of its own, not a reading of the power state. A VM
-- suspended for billing is powered off, and "stopped" is exactly the state its
-- owner is allowed to start from; the flag is what the guards check.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS suspended BOOLEAN NOT NULL DEFAULT false;
UPDATE vms SET suspended = true WHERE status = 'suspended';
