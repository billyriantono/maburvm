-- The plan a VM was ordered on, and the billing service that pays for it
-- (the billing system's own reference, e.g. its service number).
ALTER TABLE vms ADD COLUMN IF NOT EXISTS plan_id UUID;
ALTER TABLE vms ADD COLUMN IF NOT EXISTS billing_ref VARCHAR(255);
CREATE INDEX IF NOT EXISTS idx_vms_billing_ref ON vms (billing_ref) WHERE billing_ref IS NOT NULL;

-- A suspended account cannot sign in, and its sessions and API keys stop
-- working, until an administrator lifts it.
ALTER TABLE users ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ;

-- Hostnames are unique per owner and checked by the panel. A database built
-- from an older schema may still hold a unique index or constraint on the
-- hostname alone, which would refuse two customers the same name.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT i.indexrelid::regclass::text AS index_name, c.conname
        FROM pg_index i
        LEFT JOIN pg_constraint c ON c.conindid = i.indexrelid AND c.contype = 'u'
        WHERE i.indrelid = 'vms'::regclass
          AND i.indisunique AND NOT i.indisprimary
          AND pg_get_indexdef(i.indexrelid) ~* '\((lower\()?\(?"?hostname"?\)?(::text)?\)?\)'
    LOOP
        IF r.conname IS NOT NULL THEN
            EXECUTE format('ALTER TABLE vms DROP CONSTRAINT %I', r.conname);
        ELSE
            EXECUTE 'DROP INDEX ' || r.index_name;
        END IF;
    END LOOP;
END $$;
