DROP INDEX IF EXISTS idx_bw_vm_period_all;
DROP TABLE IF EXISTS abuse_events;
ALTER TABLE guest_connections
    DROP COLUMN IF EXISTS syn_in_total,
    DROP COLUMN IF EXISTS syn_in_rate,
    DROP COLUMN IF EXISTS packets_out_total,
    DROP COLUMN IF EXISTS packets_in_total,
    DROP COLUMN IF EXISTS pps_out,
    DROP COLUMN IF EXISTS pps_in;
DROP TABLE IF EXISTS vm_prefix_ptrs;
ALTER TABLE ip_pools DROP COLUMN IF EXISTS rdns_zone;
DROP TABLE IF EXISTS vm_metrics_daily;
ALTER TABLE users DROP COLUMN IF EXISTS transfer_pooled, DROP COLUMN IF EXISTS transfer_reset_day;
ALTER TABLE plans DROP COLUMN IF EXISTS transfer_counts, DROP COLUMN IF EXISTS transfer_reset_day;
DROP TABLE IF EXISTS vm_traffic_counters;
