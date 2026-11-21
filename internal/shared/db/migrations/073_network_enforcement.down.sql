DROP TRIGGER IF EXISTS trg_ip_history_vms ON vms;
DROP TRIGGER IF EXISTS trg_ip_history_ipv6_prefixes ON vm_ipv6_prefixes;
DROP TRIGGER IF EXISTS trg_ip_history_ip_addresses ON ip_addresses;
DROP TRIGGER IF EXISTS trg_ip_history_networks ON networks;
DROP FUNCTION IF EXISTS ip_history_vms();
DROP FUNCTION IF EXISTS ip_history_ipv6_prefixes();
DROP FUNCTION IF EXISTS ip_history_ip_addresses();
DROP FUNCTION IF EXISTS ip_history_networks();
DROP FUNCTION IF EXISTS ip_history_close(INET, TEXT);
DROP FUNCTION IF EXISTS ip_history_open(INET, TEXT, TEXT);
DROP TABLE IF EXISTS ip_assignment_history;

ALTER TABLE bandwidth_usages DROP COLUMN IF EXISTS warned_percent;
ALTER TABLE vms DROP COLUMN IF EXISTS guest_network_pending;
ALTER TABLE vms DROP COLUMN IF EXISTS guest_network_note;
ALTER TABLE vms DROP COLUMN IF EXISTS smtp_blocked;
ALTER TABLE plans DROP COLUMN IF EXISTS smtp_policy;
ALTER TABLE vms DROP COLUMN IF EXISTS firewall_default;

UPDATE firewall_rules SET action = 'deny' WHERE action = 'reject';
ALTER TABLE firewall_rules DROP CONSTRAINT IF EXISTS firewall_rules_action_check;
ALTER TABLE firewall_rules ADD CONSTRAINT firewall_rules_action_check CHECK (action IN ('allow', 'deny'));
ALTER TABLE firewall_rules DROP COLUMN IF EXISTS family;
ALTER TABLE firewall_rules DROP COLUMN IF EXISTS log;
ALTER TABLE firewall_rules DROP COLUMN IF EXISTS icmp_type;
