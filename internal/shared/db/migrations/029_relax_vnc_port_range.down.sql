ALTER TABLE vms DROP CONSTRAINT IF EXISTS vms_vnc_port_check;
ALTER TABLE vms ADD CONSTRAINT vms_vnc_port_check
    CHECK (vnc_port >= 5900 AND vnc_port <= 5999);
