-- Shared storage pools: storage several nodes see at the same path (an NFS
-- export the agents mount, or a directory the operator mounts on each node).
-- Each member node keeps its own storage_pools row pointing here, so nothing
-- changes for pools that are not shared.
CREATE TABLE IF NOT EXISTS shared_storage_pools (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(100) NOT NULL,
    kind varchar(16) NOT NULL DEFAULT 'mounted',
    path varchar(255) NOT NULL,
    nfs_server varchar(255) NOT NULL DEFAULT '',
    nfs_export varchar(255) NOT NULL DEFAULT '',
    mount_options varchar(255) NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_shared_storage_pools_name ON shared_storage_pools(name) WHERE deleted_at IS NULL;

ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS shared_pool_id uuid;
CREATE INDEX IF NOT EXISTS idx_storage_pools_shared_pool ON storage_pools(shared_pool_id) WHERE shared_pool_id IS NOT NULL;

-- The disk files a move left in place because both nodes use them.
ALTER TABLE IF EXISTS vm_migrations ADD COLUMN IF NOT EXISTS kept_paths text NOT NULL DEFAULT '';
