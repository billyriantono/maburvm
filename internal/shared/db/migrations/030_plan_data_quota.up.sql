-- Plan-level monthly DATA quota + over-quota policy, and the runtime fields the
-- enforcer needs on each VM's network interface. Forward ALTERs with IF NOT
-- EXISTS so they apply cleanly to the drifted live DB (see db-schema-drift).

-- Plans: how much monthly transfer the flavor sells, and what happens after.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS data_quota_gb       BIGINT  NOT NULL DEFAULT 0;   -- 0 = unlimited
ALTER TABLE plans ADD COLUMN IF NOT EXISTS over_quota_policy   VARCHAR(20) NOT NULL DEFAULT 'throttle'; -- throttle|overage|suspend
ALTER TABLE plans ADD COLUMN IF NOT EXISTS throttle_speed_mbps INTEGER NOT NULL DEFAULT 0;   -- speed after quota when policy=throttle (0 = a low default)

-- Networks: the per-VM snapshot the enforcer reads (inherited from the plan at
-- create), plus a runtime flag so a throttled VM is restored to its normal
-- speed when its quota resets.
ALTER TABLE networks ADD COLUMN IF NOT EXISTS over_quota_policy   VARCHAR(20) NOT NULL DEFAULT 'throttle';
ALTER TABLE networks ADD COLUMN IF NOT EXISTS throttle_speed_mbps INTEGER NOT NULL DEFAULT 0;
ALTER TABLE networks ADD COLUMN IF NOT EXISTS throttled           BOOLEAN NOT NULL DEFAULT FALSE;
