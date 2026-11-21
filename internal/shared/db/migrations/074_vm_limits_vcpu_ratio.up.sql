-- CPU and disk caps per VM, copied from its plan or set by an operator. Empty
-- means no limit, which is what every existing VM and plan keeps.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS limits JSONB NOT NULL DEFAULT '{}';
ALTER TABLE vms ADD COLUMN IF NOT EXISTS limits JSONB NOT NULL DEFAULT '{}';
-- An operator's throttle on top of the VM's own limits; NULL = not throttled.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS throttle JSONB;
-- Why the node does not have the VM's limits yet; NULL = it has them.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS limits_note TEXT;

-- vCPU overcommit: the logical CPUs a node reported (0 = not measured yet) and
-- its own ratio (0 = the panel-wide one, which defaults to no cap).
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS cpu_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS vcpu_ratio DOUBLE PRECISION NOT NULL DEFAULT 0;
