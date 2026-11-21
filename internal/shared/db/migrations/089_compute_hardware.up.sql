-- Virtual hardware beyond a VM's size: what a plan gives its VMs, what a VM was
-- given, and why its node does not have it yet. '{}' is a plain VM, as every
-- VM was before.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS hardware jsonb NOT NULL DEFAULT '{}';
ALTER TABLE vms ADD COLUMN IF NOT EXISTS hardware jsonb NOT NULL DEFAULT '{}';
ALTER TABLE vms ADD COLUMN IF NOT EXISTS hardware_note text;

-- How a VM in rescue mode was put there: 'iso' (a rescue ISO in its drive) or
-- 'image' (the built-in rescue system). '' with rescue_mode on is an ISO.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS rescue_kind varchar(8) NOT NULL DEFAULT '';

-- What an image needs beyond UEFI: Secure Boot and a TPM.
ALTER TABLE os_templates
    ADD COLUMN IF NOT EXISTS secure_boot boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS tpm boolean NOT NULL DEFAULT false;

-- A node's pool of host cores for dedicated-CPU VMs, as a CPU list ("4-15").
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS dedicated_cpus varchar(255) NOT NULL DEFAULT '';

-- Snapshots without the VM's memory, and the schedule that took one.
ALTER TABLE snapshots
    ADD COLUMN IF NOT EXISTS disk_only boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS schedule_id uuid;

-- One schedule per VM: a snapshot every interval_hours, keeping the newest
-- `keep` of the snapshots it took.
CREATE TABLE IF NOT EXISTS snapshot_schedules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id uuid NOT NULL,
    interval_hours integer NOT NULL,
    keep integer NOT NULL,
    disk_only boolean NOT NULL DEFAULT true,
    quiesce boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    next_run_at timestamptz NOT NULL,
    last_run_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT snapshot_schedules_interval_check CHECK (interval_hours BETWEEN 1 AND 720),
    CONSTRAINT snapshot_schedules_keep_check CHECK (keep BETWEEN 1 AND 50)
);
CREATE UNIQUE INDEX IF NOT EXISTS snapshot_schedules_vm_idx ON snapshot_schedules (vm_id);
CREATE INDEX IF NOT EXISTS snapshot_schedules_due_idx ON snapshot_schedules (next_run_at) WHERE enabled;
