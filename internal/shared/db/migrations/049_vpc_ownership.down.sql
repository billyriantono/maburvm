DROP INDEX IF EXISTS idx_managed_networks_vpc;
DROP INDEX IF EXISTS idx_managed_networks_user;
ALTER TABLE managed_networks DROP COLUMN IF EXISTS user_id;
