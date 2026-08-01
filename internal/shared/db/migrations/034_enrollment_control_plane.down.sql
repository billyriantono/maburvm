-- =============================================================================
-- 034_enrollment_control_plane.down.sql  --  FORWARD-ONLY POLICY: FAIL CLOSED
-- =============================================================================
-- Phase 1A Gate 1 forward-only migration policy (operator-selected).
--
-- Applied migrations are IMMUTABLE. The built-in migration runner applies ONLY
-- *.up.sql files and NEVER executes this file.
--
-- This down script therefore performs NO destructive operation. The previous
-- revision of this file dropped password_reset_tokens, registration_invites,
-- smtp_config_state, smtp_config_revisions, public_url_state and
-- public_url_revisions; under the forward-only policy that DROP-based rollback
-- is unsafe and misleading and has been REMOVED. The script now FAILS CLOSED so
-- no operator can mistake it for a supported rollback path.
--
-- To undo the effects of migration 034, operators MUST:
--   1. Restore from a pre-034 (or other known-good) database backup, OR
--   2. Apply a NEW forward corrective migration (036+) that supersedes the
--      schema/semantics introduced by 034.
--
-- See docs/MIGRATION_RECOVERY.md for the full operator recovery procedure.
-- =============================================================================

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY POLICY: migration 034_enrollment_control_plane is immutable '
        'and cannot be rolled back by this down script. The built-in runner is '
        'up-only and never executes *.down.sql. To recover from 034, restore a '
        'pre-034 backup or apply a forward corrective migration (036+). See '
        'docs/MIGRATION_RECOVERY.md.';
END
$$;
