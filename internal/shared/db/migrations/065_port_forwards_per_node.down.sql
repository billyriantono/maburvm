-- The fleet-wide index is not put back: forwards created since may share a
-- port across nodes, and it would fail to build.
DROP INDEX IF EXISTS idx_port_forwards_node_port;
ALTER TABLE port_forwards DROP COLUMN IF EXISTS node_id;
