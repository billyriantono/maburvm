-- Time-series of node resource usage samples for historical monitoring.
CREATE TABLE IF NOT EXISTS node_metrics (
    id                       BIGSERIAL PRIMARY KEY,
    node_id                  UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    cpu_usage                DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_usage             DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_usage               DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_rx_bytes_per_sec DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_tx_bytes_per_sec DOUBLE PRECISION NOT NULL DEFAULT 0,
    vm_count                 INTEGER NOT NULL DEFAULT 0,
    status                   VARCHAR(20) NOT NULL DEFAULT '',
    recorded_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_node_metrics_node_time ON node_metrics(node_id, recorded_at DESC);
