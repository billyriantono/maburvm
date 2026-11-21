-- Code before 095 does not know RBD and would take an image for a file, so
-- rolling back is refused while a VM has a disk on RBD: move or delete it
-- first. The RBD pools' rows then go, since that code would read their
-- paths as directories.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM vms v JOIN storage_pools p ON CAST(p.id AS TEXT) = CAST(v.storage_pool_id AS TEXT)
               WHERE v.deleted_at IS NULL AND p.type = 'rbd')
       OR EXISTS (SELECT 1 FROM vm_disks d WHERE d.deleted_at IS NULL AND d.path LIKE 'rbd://%') THEN
        RAISE EXCEPTION 'VMs have disks on Ceph RBD pools: move or delete them before rolling back past 095';
    END IF;
END $$;
DELETE FROM storage_pools WHERE type = 'rbd' AND CAST(shared_pool_id AS TEXT) IN
    (SELECT CAST(id AS TEXT) FROM shared_storage_pools WHERE kind = 'rbd');
DELETE FROM shared_storage_pools WHERE kind = 'rbd';
DROP INDEX IF EXISTS idx_shared_storage_pools_ceph;
ALTER TABLE IF EXISTS shared_storage_pools DROP COLUMN IF EXISTS rbd_namespace;
ALTER TABLE IF EXISTS shared_storage_pools DROP COLUMN IF EXISTS rbd_pool;
ALTER TABLE IF EXISTS shared_storage_pools DROP COLUMN IF EXISTS ceph_cluster_id;
DROP TABLE IF EXISTS ceph_cluster_nodes;
DROP TABLE IF EXISTS ceph_clusters;
