-- A plan's display price and its per-VM allowances. Every column is nullable or
-- defaulted, so existing plans read as "no price shown, no limit".
ALTER TABLE plans ADD COLUMN IF NOT EXISTS price_monthly NUMERIC(14,4);
ALTER TABLE plans ADD COLUMN IF NOT EXISTS price_hourly NUMERIC(14,4);
ALTER TABLE plans ADD COLUMN IF NOT EXISTS currency VARCHAR(3) NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_snapshots INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_backups INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_extra_ipv4 INTEGER;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS max_extra_ipv6 INTEGER;

-- The VMs on a plan, for applying a plan's changes to them.
CREATE INDEX IF NOT EXISTS idx_vms_plan_id ON vms (plan_id) WHERE plan_id IS NOT NULL;
