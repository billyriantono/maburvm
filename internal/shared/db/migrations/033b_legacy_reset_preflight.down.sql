-- =============================================================================
-- 033b_legacy_reset_preflight.down.sql  --  FORWARD-ONLY POLICY: FAIL CLOSED
-- =============================================================================
-- Phase 1A Gate 1 forward-only migration policy (operator-selected).
--
-- Applied migrations are IMMUTABLE. The built-in migration runner applies ONLY
-- *.up.sql files and NEVER executes this file.
--
-- Migration 033b only *revoked* the legacy raw-token password_reset_tokens table
-- so that 034 could create the canonical hash-only table; there is nothing safe
-- to reverse here. This down script therefore performs NO operation (no DROP /
-- ALTER of data) and FAILS CLOSED so it cannot be mistaken for a supported
-- rollback. Revoked legacy reset links contained raw bearer tokens and must
-- never be resurrected.
--
-- To undo the effects of migration 033b, operators MUST:
--   1. Restore from a pre-033b (or other known-good) database backup, OR
--   2. Apply a NEW forward corrective migration (036+) if remediation is needed.
--
-- See docs/MIGRATION_RECOVERY.md for the full operator recovery procedure.
-- =============================================================================

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY POLICY: migration 033b_legacy_reset_preflight is immutable '
        'and cannot be rolled back by this down script. The built-in runner is '
        'up-only and never executes *.down.sql. Revoked legacy raw-token reset '
        'links are intentionally not recoverable. To recover, restore a pre-033b '
        'backup or apply a forward corrective migration (036+). See '
        'docs/MIGRATION_RECOVERY.md.';
END
$$;
