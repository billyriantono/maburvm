-- Images: user-owned, standalone disk images stored in object storage (R2/S3).
-- Unlike snapshots (libvirt-internal, die with the VM) and backups (FK-cascaded
-- to the VM), an image SURVIVES deletion of its source VM — source_vm_id is set
-- NULL when the VM is removed — so it can seed a brand-new VM later, exactly like
-- a Vultr/DigitalOcean snapshot.
CREATE TABLE IF NOT EXISTS images (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name           VARCHAR(255) NOT NULL DEFAULT '',
    -- Source VM this image was captured from. SET NULL on VM delete so the image
    -- outlives the VM.
    source_vm_id   UUID REFERENCES vms(id) ON DELETE SET NULL,
    -- Base OS template, carried so a create-from-image knows which OS it is.
    os_template_id UUID REFERENCES os_templates(id) ON DELETE SET NULL,
    -- Object-storage key of the exported compressed qcow2 (images/<id>.qcow2).
    object_key     VARCHAR(500) NOT NULL DEFAULT '',
    size_bytes     BIGINT NOT NULL DEFAULT 0,
    checksum       VARCHAR(64) NOT NULL DEFAULT '',
    -- pending → available (export finished) | failed
    status         VARCHAR(20) NOT NULL DEFAULT 'pending',
    error_message  TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_images_user_id ON images(user_id);
CREATE INDEX IF NOT EXISTS idx_images_status ON images(status);
CREATE INDEX IF NOT EXISTS idx_images_deleted_at ON images(deleted_at);
