-- Add a per-VM toggle for VNC console access (Virtualizor parity: enable/disable
-- console). Existing VMs default to enabled so behaviour is unchanged.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS console_enabled boolean NOT NULL DEFAULT true;
