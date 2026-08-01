-- =============================================================================
-- 035_enrollment_integrity.down.sql  --  FORWARD-ONLY POLICY: FAIL CLOSED
-- =============================================================================
-- Phase 1A Gate 1 forward-only migration policy (operator-selected).
--
-- Applied migrations are IMMUTABLE. The built-in migration runner applies ONLY
-- *.up.sql files and NEVER executes this file.
--
-- This down script therefore performs NO destructive operation. The previous
-- revision dropped triggers, functions, CHECK/UNIQUE constraints, reverted
-- RESTRICT FKs, deleted singleton state rows and dropped sequences introduced by
-- 035; under the forward-only policy that DROP/ALTER-based rollback is unsafe and
-- misleading and has been REMOVED. The script now FAILS CLOSED so no operator can
-- mistake it for a supported rollback path.
--
-- To undo the effects of migration 035, operators MUST:
--   1. Restore from a pre-035 (or other known-good) database backup, OR
--   2. Apply a NEW forward corrective migration (036/037) that supersedes the
--      integrity invariants introduced by 035.
--
-- See docs/MIGRATION_RECOVERY.md for the full operator recovery procedure.
-- =============================================================================

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY POLICY: migration 035_enrollment_integrity is immutable and '
        'cannot be rolled back by this down script. The built-in runner is '
        'up-only and never executes *.down.sql. To recover from 035, restore a '
        'pre-035 backup or apply a forward corrective migration (036/037). See '
        'docs/MIGRATION_RECOVERY.md.';
END
$$;
