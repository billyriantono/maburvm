-- Floating IPs (Phase 1: same-node, L3/1:1 NAT).
--
-- Deliberately extends ip_addresses instead of adding a parallel floating_ips
-- table: converting an address between floating (host NAT) and direct (bound in
-- the guest) is then a single UPDATE, not a cross-table row move with a window
-- where the address exists in both tables or neither.
--
--   delivery_mode  'direct'   = bridged/bound in the guest (every pre-existing address)
--                  'floating' = lives on the host, NATed to the VM's address
--   nat_mode       'inbound'  = DNAT only; VM egresses under its own identity
--                  'full'     = DNAT + SNAT; VM egresses as the floating IP
--   user_id        tenant owner of a floating IP while it is attached to no VM
--                  (ownership is otherwise implied via vm_id)
ALTER TABLE ip_addresses
    ADD COLUMN IF NOT EXISTS delivery_mode VARCHAR(16) NOT NULL DEFAULT 'direct',
    ADD COLUMN IF NOT EXISTS nat_mode      VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS user_id       UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_ip_addresses_floating
    ON ip_addresses (delivery_mode) WHERE delivery_mode = 'floating';
CREATE INDEX IF NOT EXISTS idx_ip_addresses_user ON ip_addresses (user_id);
