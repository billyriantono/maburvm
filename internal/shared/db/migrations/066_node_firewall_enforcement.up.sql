-- Whether a node passes bridged guest traffic through its firewall, as its
-- agent last reported: 'enforced', 'off', or '' when it has not said (an agent
-- older than the report). Written by the metrics collector.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS firewall_enforcement VARCHAR(16) NOT NULL DEFAULT '';
