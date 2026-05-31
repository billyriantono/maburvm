-- Reverse DNS (PTR) hostname for managed IP addresses.
ALTER TABLE ip_addresses ADD COLUMN IF NOT EXISTS rdns VARCHAR(253) NOT NULL DEFAULT '';
