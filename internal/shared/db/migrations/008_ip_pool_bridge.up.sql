-- IP Pool host bridge: the Linux bridge a VM's NIC attaches to when it receives
-- an address from this pool. Empty means the node default (e.g. virbr0).
ALTER TABLE ip_pools ADD COLUMN IF NOT EXISTS bridge VARCHAR(64);
