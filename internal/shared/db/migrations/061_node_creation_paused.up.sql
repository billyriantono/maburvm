-- A node can stop taking new VMs while the ones it already runs keep working.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS creation_paused BOOLEAN NOT NULL DEFAULT false;
