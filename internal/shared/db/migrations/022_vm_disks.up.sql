-- Extra data disks attached to a VM (parity). The primary boot disk
-- is not tracked here — only additional disks managed via the disks API.
CREATE TABLE IF NOT EXISTS vm_disks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id      UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    device     VARCHAR(16) NOT NULL, -- virtio target, e.g. vdb
    size_gb    INTEGER NOT NULL,
    path       TEXT NOT NULL,        -- backing volume path on the node
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vm_disks_vm ON vm_disks(vm_id) WHERE deleted_at IS NULL;
-- One row per (vm, device) so we never double-assign a target slot.
CREATE UNIQUE INDEX IF NOT EXISTS uq_vm_disks_vm_device ON vm_disks(vm_id, device) WHERE deleted_at IS NULL;
