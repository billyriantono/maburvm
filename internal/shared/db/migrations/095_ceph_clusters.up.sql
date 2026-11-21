-- Existing Ceph clusters staff register, and the nodes each is enabled on.
-- Nothing here changes a node, a VM or a pool that does not use Ceph.
CREATE TABLE IF NOT EXISTS ceph_clusters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(100) NOT NULL,
    -- monitors: comma-separated host or host:port.
    monitors text NOT NULL,
    -- client_id: the cephx user without "client."; client_key is sealed with
    -- the panel's AES key.
    client_id varchar(64) NOT NULL,
    client_key text NOT NULL,
    -- fsid: given by staff, or recorded from the first node that reaches the
    -- cluster. Every other node must report the same one.
    fsid varchar(36) NOT NULL DEFAULT '',
    -- What the last node to answer reported.
    health varchar(16) NOT NULL DEFAULT '',
    health_summary text NOT NULL DEFAULT '',
    total_bytes bigint NOT NULL DEFAULT 0,
    used_bytes bigint NOT NULL DEFAULT 0,
    available_bytes bigint NOT NULL DEFAULT 0,
    checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ceph_clusters_name ON ceph_clusters(name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS ceph_cluster_nodes (
    cluster_id uuid NOT NULL,
    node_id uuid NOT NULL,
    -- ok, unreachable (the node cannot reach the cluster), mismatch (the node
    -- reaches another cluster).
    status varchar(16) NOT NULL DEFAULT 'ok',
    status_note text NOT NULL DEFAULT '',
    checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cluster_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_ceph_cluster_nodes_node ON ceph_cluster_nodes(node_id);

-- A shared pool of kind rbd is one RBD pool (and namespace) of a cluster; its
-- members are the nodes the cluster is enabled on.
ALTER TABLE shared_storage_pools ADD COLUMN IF NOT EXISTS ceph_cluster_id uuid;
ALTER TABLE shared_storage_pools ADD COLUMN IF NOT EXISTS rbd_pool varchar(128) NOT NULL DEFAULT '';
ALTER TABLE shared_storage_pools ADD COLUMN IF NOT EXISTS rbd_namespace varchar(128) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_shared_storage_pools_ceph ON shared_storage_pools(ceph_cluster_id) WHERE ceph_cluster_id IS NOT NULL;
