-- 033b_legacy_reset_preflight: pre-034 legacy password-reset table revocation.
--
-- Sort order: this file is named so it is lexicographically ordered AFTER
-- 033_quota_policy_foundation and BEFORE 034_enrollment_control_plane. The
-- migration runner records each version in schema_migrations and SKIPS already
-- applied versions, so this follow-up must be idempotent and must never rewrite
-- 034 (which may already be recorded on some installs).
--
-- It handles four mutually exclusive states for the `password_reset_tokens`
-- relation and fails closed on anything ambiguous rather than silently
-- reinterpreting or migrating raw tokens:
--
--   1. table absent
--        -> no-op. 034 will create the canonical hash-only table next.
--   2. canonical table present (has `token_hash`)
--        -> no-op. 034 already owns the canonical schema.
--   3. exact legacy raw-token shape present (has `token`, lacks `token_hash`)
--      AND 034 not yet recorded
--        -> revoke legacy reset links by dropping ONLY that exact table,
--           WITHOUT CASCADE, transactionally, so 034 creates the canonical
--           hash-only table immediately after.
--   4. legacy shape present AFTER 034 already recorded, OR any unknown/mixed
--      shape (e.g. has both `token` and `token_hash`, or neither)
--        -> fail closed with an actionable remediation message. We must NOT
--           drop here because 034 is already recorded and would be SKIPPED,
--           leaving the system with no reset table.
--
-- This whole file executes inside the runner's single transaction, so the
-- DROP (case 3) is transactional.

DO $$
DECLARE
    v_table_exists      boolean;
    v_has_token_hash    boolean;
    v_has_legacy_token  boolean;
    v_034_recorded      boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'password_reset_tokens'
    ) INTO v_table_exists;

    -- Case 1: table absent -> no-op.
    IF NOT v_table_exists THEN
        RETURN;
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'password_reset_tokens'
          AND column_name = 'token_hash'
    ) INTO v_has_token_hash;

    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'password_reset_tokens'
          AND column_name = 'token'
    ) INTO v_has_legacy_token;

    -- Case 2: canonical table present -> no-op.
    IF v_has_token_hash THEN
        RETURN;
    END IF;

    -- Determine whether 034 has already been recorded by the runner.
    SELECT EXISTS (
        SELECT 1 FROM schema_migrations
        WHERE version = '034_enrollment_control_plane'
    ) INTO v_034_recorded;

    -- Case 4a: legacy shape present but 034 already recorded -> fail closed.
    -- Dropping now would leave no reset table because 034 will be skipped.
    IF v_034_recorded THEN
        RAISE EXCEPTION
            'Legacy raw-token password_reset_tokens table still present AFTER '
            'migration 034 was recorded. 034''s CREATE TABLE IF NOT EXISTS was a '
            'no-op, so the canonical hash-only table was never built. Manual '
            'remediation required: back up and DROP the legacy password_reset_tokens '
            'table, then re-run migration 034 (or apply a targeted fix) so the '
            'canonical schema is created. Raw reset tokens are NOT migrated.';
    END IF;

    -- Case 3: exact legacy raw-token shape (has `token`, lacks `token_hash`)
    -- and 034 not yet recorded -> revoke legacy links by dropping ONLY this
    -- exact table, no CASCADE. Nothing references this table, so plain DROP
    -- is safe and cannot cascade to unrelated objects.
    IF v_has_legacy_token AND NOT v_has_token_hash THEN
        DROP TABLE password_reset_tokens;
        RETURN;
    END IF;

    -- Case 4b: unknown/mixed shape -> fail closed.
    RAISE EXCEPTION
        'password_reset_tokens has an unexpected shape (neither canonical '
        'token_hash nor a clean legacy token column). Raw tokens are never '
        'silently reinterpreted or migrated. Manual remediation required before '
        'applying migration 034.';
END
$$;
