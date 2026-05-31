-- Track whether a VM is currently in rescue mode (booted from a rescue ISO).
ALTER TABLE vms ADD COLUMN IF NOT EXISTS rescue_mode BOOLEAN NOT NULL DEFAULT FALSE;
