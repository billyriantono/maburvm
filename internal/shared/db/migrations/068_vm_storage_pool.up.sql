-- The storage pool a VM's disks were created in, so disk allocation can be
-- counted per pool. NULL means not recorded: the VM then counts against the
-- pool its node provisions into.
--
-- Text, and compared as text below: on databases built from the older
-- migration set, storage_pools.id and node_id are varchar of unknown length,
-- not uuid.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS storage_pool_id TEXT;

-- Existing VMs get the pool their node provisions into today. That is where
-- their disks are unless the node's primary pool was changed after they were
-- created.
UPDATE vms v
SET storage_pool_id = (
    SELECT p.id::text
    FROM storage_pools p
    WHERE p.node_id::text = v.node_id::text AND p.deleted_at IS NULL AND p.is_primary
    ORDER BY p.created_at
    LIMIT 1
)
WHERE v.storage_pool_id IS NULL AND v.deleted_at IS NULL;
