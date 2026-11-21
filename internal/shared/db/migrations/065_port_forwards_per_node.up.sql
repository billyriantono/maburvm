-- A port forward takes a port on the node its VM runs on, so that is where it
-- has to be unique. The old index was fleet-wide and included the source, which
-- let two customers hold the same port on one node (whichever rule was applied
-- last lost) and let one customer's forward block that port on every node.
ALTER TABLE port_forwards ADD COLUMN IF NOT EXISTS node_id UUID;

UPDATE port_forwards SET protocol = 'tcp' WHERE protocol IS NULL;

-- Forwards of VMs that are gone were never removed; they hold a port for nobody.
UPDATE port_forwards pf SET deleted_at = NOW()
 WHERE pf.deleted_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM vms v WHERE v.id::text = pf.vm_id::text AND v.deleted_at IS NULL);

-- Compared and assigned as text: an older schema may hold these ids as varchar.
UPDATE port_forwards pf SET node_id = NULLIF(v.node_id::text, '')::uuid
  FROM vms v
 WHERE v.id::text = pf.vm_id::text AND pf.node_id::text IS DISTINCT FROM NULLIF(v.node_id::text, '');

-- Where the old index let several forwards share a node's port, the oldest
-- keeps it. The others never worked reliably and are retired.
UPDATE port_forwards pf SET deleted_at = NOW()
 WHERE pf.deleted_at IS NULL
   AND EXISTS (
       SELECT 1 FROM port_forwards o
        WHERE o.deleted_at IS NULL
          AND o.node_id = pf.node_id
          AND o.external_port = pf.external_port
          AND o.protocol = pf.protocol
          AND (COALESCE(o.created_at, 'epoch'), o.id::text) < (COALESCE(pf.created_at, 'epoch'), pf.id::text));

-- Drop every earlier uniqueness rule on the table, whatever it was called: a
-- database built from an older migration set may carry a different one.
DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT conname FROM pg_constraint
              WHERE conrelid = 'port_forwards'::regclass AND contype = 'u' LOOP
        EXECUTE format('ALTER TABLE port_forwards DROP CONSTRAINT %I', r.conname);
    END LOOP;
    FOR r IN SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
              WHERE i.indrelid = 'port_forwards'::regclass AND i.indisunique AND NOT i.indisprimary LOOP
        EXECUTE format('DROP INDEX IF EXISTS %I', r.relname);
    END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_port_forwards_node_port
    ON port_forwards(node_id, external_port, protocol) WHERE deleted_at IS NULL;
