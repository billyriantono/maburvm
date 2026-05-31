-- Time-series of per-VM resource usage samples for historical monitoring.
CREATE TABLE IF NOT EXISTS vm_metrics (
    id                       BIGSERIAL PRIMARY KEY,
    vm_id                    UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    cpu_usage                DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_usage             DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_used_bytes        BIGINT NOT NULL DEFAULT 0,
    disk_read_bytes_per_sec  BIGINT NOT NULL DEFAULT 0,
    disk_write_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    network_rx_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    network_tx_bytes_per_sec BIGINT NOT NULL DEFAULT 0,
    recorded_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vm_metrics_vm_time ON vm_metrics(vm_id, recorded_at DESC);
