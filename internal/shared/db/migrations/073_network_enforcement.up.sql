-- Firewall rules: reject as an action, an ICMP type, logging, and a family.
ALTER TABLE firewall_rules ADD COLUMN IF NOT EXISTS icmp_type VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE firewall_rules ADD COLUMN IF NOT EXISTS log BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE firewall_rules ADD COLUMN IF NOT EXISTS family VARCHAR(4) NOT NULL DEFAULT '';
-- Whatever the action check is called on this database, it allowed only
-- allow and deny.
DO $$
DECLARE c record;
BEGIN
    FOR c IN SELECT conname FROM pg_constraint
             WHERE conrelid = 'firewall_rules'::regclass AND contype = 'c'
               AND pg_get_constraintdef(oid) ILIKE '%action%'
    LOOP
        EXECUTE format('ALTER TABLE firewall_rules DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;
-- NOT VALID: rows already stored are not re-checked, so an odd legacy value
-- cannot stop the panel from starting.
ALTER TABLE firewall_rules ADD CONSTRAINT firewall_rules_action_check CHECK (action IN ('allow', 'deny', 'reject')) NOT VALID;

-- What a VM's firewall does with inbound traffic no rule matched. A VM with
-- no rules today is not filtered at all, and adding its first rule used to
-- turn it into default-deny on the spot; such VMs keep accepting unless the
-- operator switches them. VMs with rules are already default-deny.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS firewall_default VARCHAR(8) NOT NULL DEFAULT 'drop';
UPDATE vms v SET firewall_default = 'accept'
WHERE NOT EXISTS (SELECT 1 FROM firewall_rules f WHERE f.vm_id::text = v.id::text AND f.deleted_at IS NULL);

-- Outbound mail (port 25). Existing VMs and plans keep sending mail; an
-- operator blocks it per plan (for new VMs) or per VM.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS smtp_policy VARCHAR(16) NOT NULL DEFAULT 'allowed';
ALTER TABLE vms ADD COLUMN IF NOT EXISTS smtp_blocked BOOLEAN NOT NULL DEFAULT FALSE;

-- An address change the guest has not taken yet, in words for the VM page;
-- pending says the node applies it by itself once it can.
ALTER TABLE vms ADD COLUMN IF NOT EXISTS guest_network_note TEXT;
ALTER TABLE vms ADD COLUMN IF NOT EXISTS guest_network_pending BOOLEAN NOT NULL DEFAULT FALSE;

-- The highest usage warning already sent this period. Usage already past a
-- default threshold counts as warned, so the upgrade sends no burst of mail.
ALTER TABLE bandwidth_usages ADD COLUMN IF NOT EXISTS warned_percent INTEGER NOT NULL DEFAULT 0;
UPDATE bandwidth_usages SET warned_percent = CASE
        WHEN total_bytes >= quota_bytes THEN 100
        WHEN total_bytes * 100 >= quota_bytes * 80 THEN 80
        ELSE 0 END
WHERE quota_bytes > 0 AND period_end >= CURRENT_DATE;

-- Who held an address when: one row per holding, closed when it ends. Written
-- by triggers, so every path that assigns or releases an address is covered.
-- Ids are kept as text and compared as text: on older databases some id
-- columns are varchar rather than uuid.
CREATE TABLE IF NOT EXISTS ip_assignment_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    address INET NOT NULL,
    vm_id TEXT,
    vm_hostname VARCHAR(255) NOT NULL DEFAULT '',
    user_id TEXT,
    user_email VARCHAR(255) NOT NULL DEFAULT '',
    node_id TEXT,
    kind VARCHAR(16) NOT NULL DEFAULT 'direct',
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ,
    -- Recorded when this table was created, from holdings already in place:
    -- the start time is a best estimate.
    estimated BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_ip_history_address ON ip_assignment_history USING gist (address inet_ops);
CREATE INDEX IF NOT EXISTS idx_ip_history_open ON ip_assignment_history (vm_id, address) WHERE released_at IS NULL;

CREATE OR REPLACE FUNCTION ip_history_open(p_address INET, p_vm TEXT, p_kind TEXT) RETURNS void AS $$
BEGIN
    IF p_address IS NULL OR p_vm IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM ip_assignment_history
               WHERE vm_id = p_vm AND address = p_address AND released_at IS NULL) THEN
        RETURN;
    END IF;
    INSERT INTO ip_assignment_history (address, vm_id, vm_hostname, user_id, user_email, node_id, kind)
    SELECT p_address, p_vm, COALESCE(v.hostname, ''), v.user_id::text, COALESCE(u.email, ''), v.node_id::text, p_kind
    FROM (SELECT 1) one
    LEFT JOIN vms v ON v.id::text = p_vm
    LEFT JOIN users u ON u.id::text = v.user_id::text;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ip_history_close(p_address INET, p_vm TEXT) RETURNS void AS $$
BEGIN
    IF p_address IS NULL OR p_vm IS NULL THEN
        RETURN;
    END IF;
    UPDATE ip_assignment_history SET released_at = NOW()
    WHERE vm_id = p_vm AND address = p_address AND released_at IS NULL;
END $$ LANGUAGE plpgsql;

-- A failure in the history must never fail the write it records.
CREATE OR REPLACE FUNCTION ip_history_networks() RETURNS trigger AS $$
BEGIN
    BEGIN
        IF TG_OP <> 'INSERT' AND OLD.deleted_at IS NULL THEN
            IF TG_OP = 'DELETE' THEN
                PERFORM ip_history_close(host(OLD.ip_address::inet)::inet, OLD.vm_id::text);
            ELSIF NEW.deleted_at IS NOT NULL OR NEW.ip_address IS DISTINCT FROM OLD.ip_address OR NEW.vm_id IS DISTINCT FROM OLD.vm_id THEN
                PERFORM ip_history_close(host(OLD.ip_address::inet)::inet, OLD.vm_id::text);
            END IF;
        END IF;
        IF TG_OP <> 'DELETE' AND NEW.deleted_at IS NULL THEN
            PERFORM ip_history_open(host(NEW.ip_address::inet)::inet, NEW.vm_id::text, 'direct');
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'ip history (networks): %', SQLERRM;
    END;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ip_history_ip_addresses() RETURNS trigger AS $$
BEGIN
    BEGIN
        IF TG_OP <> 'INSERT' AND OLD.vm_id IS NOT NULL AND OLD.deleted_at IS NULL THEN
            IF TG_OP = 'DELETE' THEN
                PERFORM ip_history_close(host(OLD.address::inet)::inet, OLD.vm_id::text);
            ELSIF NEW.deleted_at IS NOT NULL OR NEW.address IS DISTINCT FROM OLD.address OR NEW.vm_id IS DISTINCT FROM OLD.vm_id THEN
                PERFORM ip_history_close(host(OLD.address::inet)::inet, OLD.vm_id::text);
            END IF;
        END IF;
        IF TG_OP <> 'DELETE' AND NEW.vm_id IS NOT NULL AND NEW.deleted_at IS NULL THEN
            PERFORM ip_history_open(host(NEW.address::inet)::inet, NEW.vm_id::text,
                CASE WHEN NEW.delivery_mode = 'floating' THEN 'floating' ELSE 'direct' END);
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'ip history (ip_addresses): %', SQLERRM;
    END;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION ip_history_ipv6_prefixes() RETURNS trigger AS $$
BEGIN
    BEGIN
        IF TG_OP <> 'INSERT' THEN
            IF TG_OP = 'DELETE' OR NEW.prefix IS DISTINCT FROM OLD.prefix OR NEW.vm_id IS DISTINCT FROM OLD.vm_id THEN
                PERFORM ip_history_close(OLD.prefix::inet, OLD.vm_id::text);
            END IF;
        END IF;
        IF TG_OP <> 'DELETE' THEN
            PERFORM ip_history_open(NEW.prefix::inet, NEW.vm_id::text, 'ipv6_prefix');
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'ip history (vm_ipv6_prefixes): %', SQLERRM;
    END;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

-- A VM handed to another owner starts new rows under that owner; a deleted VM
-- holds nothing any more.
CREATE OR REPLACE FUNCTION ip_history_vms() RETURNS trigger AS $$
BEGIN
    BEGIN
        IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
            UPDATE ip_assignment_history SET released_at = NOW() WHERE vm_id = NEW.id::text AND released_at IS NULL;
        ELSIF NEW.user_id IS DISTINCT FROM OLD.user_id THEN
            WITH closed AS (
                UPDATE ip_assignment_history SET released_at = NOW()
                WHERE vm_id = NEW.id::text AND released_at IS NULL
                RETURNING address, kind
            )
            INSERT INTO ip_assignment_history (address, vm_id, vm_hostname, user_id, user_email, node_id, kind)
            SELECT c.address, NEW.id::text, COALESCE(NEW.hostname, ''), NEW.user_id::text,
                   COALESCE((SELECT email FROM users WHERE id::text = NEW.user_id::text), ''), NEW.node_id::text, c.kind
            FROM closed c;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'ip history (vms): %', SQLERRM;
    END;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_ip_history_networks ON networks;
CREATE TRIGGER trg_ip_history_networks AFTER INSERT OR UPDATE OR DELETE ON networks
    FOR EACH ROW EXECUTE FUNCTION ip_history_networks();
DROP TRIGGER IF EXISTS trg_ip_history_ip_addresses ON ip_addresses;
CREATE TRIGGER trg_ip_history_ip_addresses AFTER INSERT OR UPDATE OR DELETE ON ip_addresses
    FOR EACH ROW EXECUTE FUNCTION ip_history_ip_addresses();
DROP TRIGGER IF EXISTS trg_ip_history_ipv6_prefixes ON vm_ipv6_prefixes;
CREATE TRIGGER trg_ip_history_ipv6_prefixes AFTER INSERT OR UPDATE OR DELETE ON vm_ipv6_prefixes
    FOR EACH ROW EXECUTE FUNCTION ip_history_ipv6_prefixes();
DROP TRIGGER IF EXISTS trg_ip_history_vms ON vms;
CREATE TRIGGER trg_ip_history_vms AFTER UPDATE OF user_id, deleted_at ON vms
    FOR EACH ROW EXECUTE FUNCTION ip_history_vms();

-- What is held right now, from when it is known to have been held at the latest.
-- A database this cannot read leaves the history to start empty rather than
-- stopping the panel from starting.
DO $$
BEGIN
INSERT INTO ip_assignment_history (address, vm_id, vm_hostname, user_id, user_email, node_id, kind, assigned_at, estimated)
SELECT DISTINCT ON (h.address, h.vm_id) h.address, h.vm_id, COALESCE(v.hostname, ''), v.user_id::text, COALESCE(u.email, ''),
       v.node_id::text, h.kind, COALESCE(h.since, NOW()), TRUE
FROM (
    SELECT host(n.ip_address::inet)::inet AS address, n.vm_id::text AS vm_id, 'direct' AS kind, n.created_at::timestamptz AS since, 1 AS rank
    FROM networks n WHERE n.deleted_at IS NULL
    UNION ALL
    SELECT host(a.address::inet)::inet, a.vm_id::text, CASE WHEN a.delivery_mode = 'floating' THEN 'floating' ELSE 'direct' END,
           a.updated_at::timestamptz, 2
    FROM ip_addresses a WHERE a.vm_id IS NOT NULL AND a.deleted_at IS NULL
    UNION ALL
    SELECT p.prefix::inet, p.vm_id::text, 'ipv6_prefix', p.created_at::timestamptz, 1 FROM vm_ipv6_prefixes p
) h
JOIN vms v ON v.id::text = h.vm_id AND v.deleted_at IS NULL
LEFT JOIN users u ON u.id::text = v.user_id::text
WHERE NOT EXISTS (SELECT 1 FROM ip_assignment_history x WHERE x.vm_id = h.vm_id AND x.address = h.address AND x.released_at IS NULL)
ORDER BY h.address, h.vm_id, h.rank;
EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'ip history backfill skipped: %', SQLERRM;
END $$;
