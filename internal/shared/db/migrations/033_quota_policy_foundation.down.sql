-- =============================================================================
-- 033_quota_policy_foundation.down.sql  --  FORWARD-ONLY POLICY: FAIL CLOSED
-- =============================================================================
-- Phase 1A Gate 1 forward-only migration policy (operator-selected).
--
-- Applied migrations are IMMUTABLE. This project's built-in migration runner
-- (cmd/migrate/main.go and internal/panel/server/migrate.go) applies ONLY
-- *.up.sql files: it has no down/rollback path and NEVER executes this file.
--
-- This down script therefore performs NO destructive operation (no DROP / ALTER
-- of data). It exists only as a documented, fail-closed artifact so that no
-- operator can mistake it for a supported rollback path.
--
-- To undo the effects of migration 033, operators MUST:
--   1. Restore from a pre-033 (or other known-good) database backup, OR
--   2. Apply a NEW forward corrective migration (036+) that supersedes the
--      schema/semantics introduced by 033.
--
-- See docs/MIGRATION_RECOVERY.md for the full operator recovery procedure.
-- =============================================================================

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY POLICY: migration 033_quota_policy_foundation is immutable '
        'and cannot be rolled back by this down script. The built-in runner is '
        'up-only and never executes *.down.sql. To recover from 033, restore a '
        'pre-033 backup or apply a forward corrective migration (036+). See '
        'docs/MIGRATION_RECOVERY.md.';
END
$$;
