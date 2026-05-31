-- Add per-VM anti-spoofing flag to network interfaces (anti-IP/MAC hijacking
-- protection: libvirt nwfilter clean-traffic + iptables/ebtables). Defaults to
-- enabled so existing interfaces keep the protective behaviour.
ALTER TABLE networks ADD COLUMN IF NOT EXISTS anti_spoofing BOOLEAN NOT NULL DEFAULT true;
