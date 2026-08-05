DROP INDEX IF EXISTS idx_ip_addresses_user;
DROP INDEX IF EXISTS idx_ip_addresses_floating;
ALTER TABLE ip_addresses
    DROP COLUMN IF EXISTS user_id,
    DROP COLUMN IF EXISTS nat_mode,
    DROP COLUMN IF EXISTS delivery_mode;
