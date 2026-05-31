-- IP Pool multi-node support: junction table ip_pool_nodes
-- Allows one IP pool to be assigned to multiple nodes (or "Any Node" when no rows exist).
-- The legacy node_id column is kept for backward compatibility.

CREATE TABLE IF NOT EXISTS ip_pool_nodes (
    pool_id UUID NOT NULL REFERENCES ip_pools(id) ON DELETE CASCADE,
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (pool_id, node_id)
);

CREATE INDEX IF NOT EXISTS idx_ip_pool_nodes_node_id ON ip_pool_nodes(node_id);

-- Migrate existing node_id data into junction table
INSERT INTO ip_pool_nodes (pool_id, node_id)
SELECT id, node_id FROM ip_pools WHERE node_id IS NOT NULL
ON CONFLICT DO NOTHING;
