ALTER TABLE networks DROP COLUMN IF EXISTS throttled;
ALTER TABLE networks DROP COLUMN IF EXISTS throttle_speed_mbps;
ALTER TABLE networks DROP COLUMN IF EXISTS over_quota_policy;
ALTER TABLE plans DROP COLUMN IF EXISTS throttle_speed_mbps;
ALTER TABLE plans DROP COLUMN IF EXISTS over_quota_policy;
ALTER TABLE plans DROP COLUMN IF EXISTS data_quota_gb;
