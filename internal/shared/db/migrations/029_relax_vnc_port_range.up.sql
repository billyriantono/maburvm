-- VNC ports are auto-assigned by libvirt at start time. On a node that already
-- hosts other VMs the free port can exceed 5999 (libvirt's range runs to 65535),
-- and until the domain starts the port is unknown (stored NULL). The old
-- CHECK (5900..5999) rejected both cases: it blocked storing the real
-- auto-assigned port AND blocked NULL. Relax it to allow NULL or any valid VNC
-- port up to 65535.
ALTER TABLE vms DROP CONSTRAINT IF EXISTS vms_vnc_port_check;
ALTER TABLE vms ADD CONSTRAINT vms_vnc_port_check
    CHECK (vnc_port IS NULL OR (vnc_port >= 5900 AND vnc_port <= 65535));
