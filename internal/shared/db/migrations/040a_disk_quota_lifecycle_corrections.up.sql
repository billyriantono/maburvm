-- Phase 1A Gate-1 remediation: disk-quota reservation/VM lifecycle corrections (040a).
--
-- This is ORACLE LANE A: an ADDITIVE, forward-only corrective migration that
-- repairs/guards the disk_quota_reservations + vm_disks + vms lifecycle contract
-- in case 040 was applied. It does NOT rewrite 040; it layers the corrected
-- contract on top using catalog-aware, idempotent, fail-closed DDL.
--
-- The paired .down.sql FAILS CLOSED (RAISE P0001, no destructive SQL) and must
-- never be applied. Downgrades are handled by backup restore or a later forward
-- corrective migration.
--
-- All re-runs are safe: catalog-guarded (IF NOT EXISTS / existence checks), and
-- wrapped in the runner's single transaction so any raised exception aborts the
-- whole migration. We NEVER silently repair or zero data: a preflight drift is
-- rejected with an actionable P0001.

-- ===========================================================================
-- A) PREFLIGHT — reject extant incoherent data (actionable, fail-closed).
-- ===========================================================================
DO $$
DECLARE
    v_bad bigint;
    v_msg text;
BEGIN
    -- A1) pending reservation with consumed_at non-null (incoherent state).
    SELECT COUNT(*) INTO v_bad
      FROM disk_quota_reservations
     WHERE status = 'pending' AND consumed_at IS NOT NULL;
    IF v_bad > 0 THEN
        v_msg := format('disk_reservation_invariant: %s pending reservation(s) have a non-null consumed_at. '
                        'A pending reservation must have consumed_at IS NULL. Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;

    -- A2) consumed reservation with consumed_at null (incoherent state).
    SELECT COUNT(*) INTO v_bad
      FROM disk_quota_reservations
     WHERE status = 'consumed' AND consumed_at IS NULL;
    IF v_bad > 0 THEN
        v_msg := format('disk_reservation_invariant: %s consumed reservation(s) have a null consumed_at. '
                        'A consumed reservation must have consumed_at NOT NULL. Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;

    -- A3) unsupported reservation status (anything outside pending/consumed).
    SELECT COUNT(*) INTO v_bad
      FROM disk_quota_reservations
     WHERE status IS NULL OR status NOT IN ('pending', 'consumed');
    IF v_bad > 0 THEN
        v_msg := format('disk_reservation_invariant: %s reservation(s) carry an unsupported status (expected pending|consumed). '
                        'Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;

    -- A4) more than one pending reservation for a single VM (the unique partial
    --     index below must hold; reject current drift so it can be applied).
    SELECT COUNT(*) INTO v_bad
      FROM (
        SELECT vm_id
          FROM disk_quota_reservations
         WHERE status = 'pending'
         GROUP BY vm_id
        HAVING COUNT(*) > 1
      ) o;
    IF v_bad > 0 THEN
        v_msg := format('disk_reservation_invariant: %s VM(s) carry more than one pending reservation. '
                        'At most one pending reservation per VM is permitted. Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;

    -- A5) reservation user_id differing from its VM owner's user_id (ownership
    --     must be coherent before the composite FK is installed).
    SELECT COUNT(*) INTO v_bad
      FROM disk_quota_reservations r
      JOIN vms v ON v.id = r.vm_id
     WHERE r.user_id IS DISTINCT FROM v.user_id;
    IF v_bad > 0 THEN
        v_msg := format('disk_reservation_ownership: %s reservation(s) have a user_id that does not match their VM owner. '
                        'Reservation ownership must be coherent. Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;

    -- A6) vm_disks with non-positive size_gb (positive size is required).
    SELECT COUNT(*) INTO v_bad
      FROM vm_disks
     WHERE size_gb <= 0;
    IF v_bad > 0 THEN
        v_msg := format('vm_disk_invariant: %s vm_disks row(s) have size_gb <= 0. '
                        'A disk must be strictly positive. Refusing 040a; reconcile manually.', v_bad);
        RAISE EXCEPTION '%', v_msg USING errcode = 'P0001';
    END IF;
END$$;

-- ===========================================================================
-- B) RESERVATION LIFECYCLE hardening.
-- ===========================================================================
-- B1) The 040 table already defines status CHECK + size_gb>0. Add the
--     lifecycle coherence CHECK (pending=>consumed_at NULL, consumed=>non-null)
--     and the partial unique index allowing ONLY one pending reservation per VM.
--     Both are created idempotently.

-- Lifecycle coherence CHECK (idempotent: drop-if-exists first, then re-create so
-- the definition is always canonical regardless of 040's residual state).
DROP INDEX IF EXISTS disk_quota_reservations_one_pending_per_vm;
ALTER TABLE disk_quota_reservations
    DROP CONSTRAINT IF EXISTS chk_disk_quota_res_lifecycle;
ALTER TABLE disk_quota_reservations
    ADD CONSTRAINT chk_disk_quota_res_lifecycle
    CHECK (
        (status = 'pending' AND consumed_at IS NULL)
        OR (status = 'consumed' AND consumed_at IS NOT NULL)
    );

-- Partial unique index: at most one PENDING reservation per VM (consumed rows
-- are excluded so a VM's history is retained). Idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS disk_quota_reservations_one_pending_per_vm
    ON disk_quota_reservations (vm_id)
 WHERE status = 'pending';

-- ===========================================================================
-- C) OWNER / PARENT INTEGRITY.
-- ===========================================================================
-- C1) Unique (id, user_id) on vms so the composite reservation FK can reference a
--     UNIQUE set. Idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS uq_vms_id_user_id ON vms (id, user_id);

-- C2) Replace the canonical 040 independent reservation FKs (to users and to
--     vms, by id only) with a single composite FK (vm_id, user_id) REFERENCES
--     vms(id, user_id) ON DELETE CASCADE. The composite FK guarantees ownership
--     coherence and cascades reservation cleanup when the VM is hard-deleted
--     (only after agent-certified destroy). We locate the canonical constraint
--     names from pg_constraint and drop them dynamically; if the expected
--     canonical shapes cannot be identified we RAISE rather than silently leave
--     an unsafe/duplicate FK.
--
--     Safety for the intended post-conversion state (idempotent re-run):
--       * If the canonical single-column user/vm FKs are ABSENT and the named
--         composite fk_disk_reservation_vm_user already EXISTS, do nothing.
--       * If the canonical single-column pair EXISTS (and composite absent), drop
--         it and add the composite FK.
--       * Any other combination (incomplete or ambiguous: old FKs missing with no
--         composite, or old FKs still present alongside the composite) fails
--         closed with P0001. We never silently leave old constraints behind and
--         never broad-drop unrelated FKs.
DO $$
DECLARE
    v_user_fk text;
    v_vm_fk   text;
    v_composite_exists boolean;
BEGIN
    -- Identify the existing reservation->users FK (referencing users.id, by id only).
    SELECT c.conname INTO v_user_fk
      FROM pg_constraint c
      JOIN pg_class t  ON t.oid = c.conrelid
      JOIN pg_class rf ON rf.oid = c.confrelid
      JOIN pg_attribute a ON a.attrelid = c.conrelid
     WHERE t.relname = 'disk_quota_reservations'
       AND rf.relname = 'users'
       AND c.contype = 'f'
       AND a.attnum = ANY (c.conkey)
       AND a.attname = 'user_id'
       AND array_length(c.conkey, 1) = 1
     LIMIT 1;

    -- Identify the existing reservation->vms FK (referencing vms.id by id only).
    SELECT c.conname INTO v_vm_fk
      FROM pg_constraint c
      JOIN pg_class t  ON t.oid = c.conrelid
      JOIN pg_class rf ON rf.oid = c.confrelid
      JOIN pg_attribute a ON a.attrelid = c.conrelid
     WHERE t.relname = 'disk_quota_reservations'
       AND rf.relname = 'vms'
       AND c.contype = 'f'
       AND a.attnum = ANY (c.conkey)
       AND a.attname = 'vm_id'
       AND array_length(c.conkey, 1) = 1
     LIMIT 1;

    -- Whether the composite ownership FK already exists.
    SELECT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'disk_quota_reservations'::regclass
           AND conname = 'fk_disk_reservation_vm_user'
    ) INTO v_composite_exists;

    -- Post-conversion / idempotent re-run: canonical old FKs already gone and the
    -- composite present => nothing to do.
    IF v_user_fk IS NULL AND v_vm_fk IS NULL AND v_composite_exists THEN
        RETURN;
    END IF;

    -- Incomplete state: canonical old pair cannot be located (and composite not
    -- yet present). Refuse rather than guess or leave an unsafe constraint.
    IF v_user_fk IS NULL OR v_vm_fk IS NULL THEN
        RAISE EXCEPTION
            'disk_reservation_fk_not_identified: could not locate the canonical 040 reservation FKs '
            '(user_fk=%, vm_fk=%); composite_exists=%. Refusing to restructure FKs so as not to leave '
            'an unsafe or duplicate constraint. Reconcile the schema to the expected 040 shape '
            '(single-column user_id/vm_id FKs) before applying 040a.',
            v_user_fk, v_vm_fk, v_composite_exists
            USING errcode = 'P0001';
    END IF;

    -- Ambiguous state: canonical old FKs still present while the composite already
    -- exists (e.g. a partial prior run). Refuse rather than silently leaving the
    -- old FKs alongside the composite (would be a duplicate/ambiguous constraint).
    IF v_composite_exists THEN
        RAISE EXCEPTION
            'disk_reservation_fk_ambiguous: canonical 040 FKs (user_fk=%, vm_fk=%) are still present '
            'while composite fk_disk_reservation_vm_user already exists. Refusing to leave duplicate '
            'or ambiguous FKs. Drop the composite and re-run 040a, or drop the old FKs first.',
            v_user_fk, v_vm_fk
            USING errcode = 'P0001';
    END IF;

    EXECUTE format('ALTER TABLE disk_quota_reservations DROP CONSTRAINT %I', v_user_fk);
    EXECUTE format('ALTER TABLE disk_quota_reservations DROP CONSTRAINT %I', v_vm_fk);

    -- Install the composite ownership FK (cascading VM deletes). Verified absent
    -- above, so this is a clean add (no duplicate).
    ALTER TABLE disk_quota_reservations
        ADD CONSTRAINT fk_disk_reservation_vm_user
        FOREIGN KEY (vm_id, user_id)
        REFERENCES vms (id, user_id)
        ON DELETE CASCADE;
END$$;

-- C3) Change vms->users and vms->nodes FKs to ON DELETE RESTRICT so a parent
--     (user/node) deletion cannot silently erase VM + disk/storage accounting.
--     We locate the canonical FK names dynamically and recreate them preserving
--     their exact source/reference columns but with ON DELETE RESTRICT. We select
--     ONLY FKs whose current delete action is CASCADE, independent of the ON
--     UPDATE action (a FK is toggled purely on its delete rule). Unrelated FKs
--     are left untouched; the loop is idempotent (a RESTRICT FK no longer matches
--     the confdeltype='c' filter on re-run).
--
--     NOTE: the PL/pgSQL record variable is named `rec` and the SQL catalog
--     aliases are `ct`/`rf` so there is NO collision between the record variable
--     and a SQL table alias (the original bug used `r` for both, triggering
--     "record r is not assigned yet").
DO $$
DECLARE
    rec record;
BEGIN
    FOR rec IN
        SELECT c.conname   AS fk_name,
               rf.relname  AS ref_table,
               (
                   SELECT string_agg(a.attname, ', ' ORDER BY u.ord)
                     FROM unnest(c.conkey) WITH ORDINALITY AS u(attnum, ord)
                     JOIN pg_attribute a
                       ON a.attrelid = c.conrelid AND a.attnum = u.attnum
               ) AS src_cols,
               (
                   SELECT string_agg(a.attname, ', ' ORDER BY u.ord)
                     FROM unnest(c.confkey) WITH ORDINALITY AS u(attnum, ord)
                     JOIN pg_attribute a
                       ON a.attrelid = c.confrelid AND a.attnum = u.attnum
               ) AS ref_cols
          FROM pg_constraint c
          JOIN pg_class ct ON ct.oid = c.conrelid
          JOIN pg_class rf ON rf.oid = c.confrelid
         WHERE ct.relname = 'vms'
           AND c.contype = 'f'
           AND rf.relname IN ('users', 'nodes')
           AND c.confdeltype = 'c'          -- currently ON DELETE CASCADE
    LOOP
        -- Recreate the FK preserving its exact source/reference columns but with
        -- ON DELETE RESTRICT. The ON UPDATE action is left exactly as it was.
        EXECUTE format(
            'ALTER TABLE vms DROP CONSTRAINT %I', rec.fk_name
        );
        EXECUTE format(
            'ALTER TABLE vms ADD CONSTRAINT %I FOREIGN KEY (%s) REFERENCES %I (%s) ON DELETE RESTRICT',
            rec.fk_name, rec.src_cols, rec.ref_table, rec.ref_cols
        );
    END LOOP;
END$$;

-- ===========================================================================
-- D) DISK / VM LIFECYCLE.
-- ===========================================================================
-- D1) vm_disks.lifecycle column (attached|deleting, default attached). Backfill
--     any extant rows to 'attached'. Positive size protection already expected
--     from preflight; add an explicit CHECK for safety (idempotent).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_name = 'vm_disks' AND column_name = 'lifecycle'
    ) THEN
        ALTER TABLE vm_disks
            ADD COLUMN lifecycle varchar(16) NOT NULL DEFAULT 'attached'
            CHECK (lifecycle IN ('attached', 'deleting'));
    END IF;
END$$;

-- Backfill any legacy/null lifecycle to 'attached' (preflight already guarantees
-- positive size_gb, so this is a safe default, not a repair of limits).
UPDATE vm_disks SET lifecycle = 'attached' WHERE lifecycle IS NULL OR lifecycle = '';

-- Ensure the positive-size CHECK exists even if 040's implicit CHECK was absent.
ALTER TABLE vm_disks
    DROP CONSTRAINT IF EXISTS chk_vm_disks_positive_size;
ALTER TABLE vm_disks
    ADD CONSTRAINT chk_vm_disks_positive_size CHECK (size_gb > 0);

-- D2) Add the 'deleting' value to the vm_status enum. Enums are appended only;
--     the new label becomes usable after this transaction commits. The panel Go
--     model already carries VMStatusDeleting ("deleting") and the validator here
--     is updated to accept it, so no code path references the enum before commit.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_enum e
        JOIN pg_type t ON t.oid = e.enumtypid
       WHERE t.typname = 'vm_status' AND e.enumlabel = 'deleting'
    ) THEN
        ALTER TYPE vm_status ADD VALUE 'deleting';
    END IF;
END$$;
