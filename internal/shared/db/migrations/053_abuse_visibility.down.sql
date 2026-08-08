DROP INDEX IF EXISTS idx_guest_connections_flagged;
DROP INDEX IF EXISTS idx_guest_connections_rate;
DROP TABLE IF EXISTS guest_connections;

ALTER TABLE node_metrics
    DROP COLUMN IF EXISTS conntrack_max,
    DROP COLUMN IF EXISTS conntrack_count;
