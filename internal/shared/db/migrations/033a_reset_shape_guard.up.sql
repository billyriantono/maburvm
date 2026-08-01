-- 033a_reset_shape_guard: fresh-install / post-upgrade reset-table shape guard.
--
-- Sort order (lexical, as the runner applies each *.up.sql in order):
--   033_quota_policy_foundation  <  033a_reset_shape_guard  <  033b_legacy_reset_preflight  <  034_enrollment_control_plane
-- i.e. 033a runs BEFORE 033b and BEFORE 034. The runner records each version in
-- schema_migrations and SKIPS already-applied versions, so 033a may ALSO be
-- applied LATER on an already-upgraded DB (where 034/035/036 are already
-- recorded and the canonical hash-only table exists) — in that case it is a
-- no-op (Case 2 below).
--
-- It inspects ONLY the `password_reset_tokens` relation (never its rows) and
-- asserts an acceptable shape BEFORE 033b and 034 legitimately touch it. It
-- NEVER migrates, drops, or reinterprets any raw bearer token.
--
-- Acceptable shapes (idempotent no-ops unless noted):
--   1. table absent
--        -> fresh install (no legacy table present yet). 034 will create the
--           canonical hash-only table. No-op here.
--   2. canonical hash-only table present (has `token_hash`, lacks `token`)
--        -> 034/035/036 already own the canonical schema (the normal state on an
--           already-upgraded DB where 033a is applied late). No-op.
--   3. EXACTLY the known pre-034 raw legacy shape present
--        -> accepted by 033a so the IMMEDIATELY-FOLLOWING 033b can drop it
--           (033b case 3), after which 034 creates the canonical hash-only
--           table. No operator action is required under the normal runner
--           sequence; 033a only validates the shape and emits a NOTICE. 033a
--           must NOT drop the table itself, because 034's CREATE TABLE IF NOT
--           EXISTS would otherwise be a no-op and the canonical schema would not
--           be built — 033b owns the drop.
--
-- Rejected (fail closed with actionable remediation):
--   * mixed shape: both `token_hash` and `token` present
--   * unknown/extra-token raw-token shape: has `token` but is NOT exactly the
--     known legacy column set (id, user_id, token, expires_at, used, created_at)
--   * any raw `token` column present after 034 already recorded (raw bearer
--     tokens must never coexist with the canonical hash-only table)
--
-- The known pre-034 raw legacy shape (column name -> data type category):
--   id          uuid (PK)
--   user_id     uuid (NOT NULL, FK to users)
--   token       varchar/uuid/uuid-ish raw bearer token (UNIQUE NOT NULL)
--   expires_at  timestamptz (NOT NULL)
--   used        boolean (default false)
--   created_at  timestamptz (NOT NULL, default NOW())
--
-- We detect "exactly the legacy shape" by enumerating the actual column set and
-- requiring it to equal the known legacy set exactly (no more, no fewer). This
-- rejects both mixed and unknown shapes in one check.

DO $$
DECLARE
    v_table_exists      boolean;
    v_has_token_hash    boolean;
    v_has_legacy_token  boolean;
    v_034_recorded      boolean;
    v_035_recorded      boolean;
    v_cols              text[];
    v_legacy_cols       text[] := ARRAY['id', 'user_id', 'token', 'expires_at', 'used', 'created_at'];
    v_legacy_set_ok     boolean;
    v_unknown_extra     boolean;
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

    -- Case 2: canonical hash-only table present (and no raw token) -> no-op.
    IF v_has_token_hash AND NOT v_has_legacy_token THEN
        RETURN;
    END IF;

    -- Determine whether 034 / 035 have already been recorded by the runner.
    SELECT EXISTS (
        SELECT 1 FROM schema_migrations WHERE version = '034_enrollment_control_plane'
    ) INTO v_034_recorded;
    SELECT EXISTS (
        SELECT 1 FROM schema_migrations WHERE version = '035_enrollment_integrity'
    ) INTO v_035_recorded;

    -- Reject: mixed shape (canonical hash AND raw token coexist). A raw bearer
    -- token must never sit beside the canonical hash-only table.
    IF v_has_token_hash AND v_has_legacy_token THEN
        RAISE EXCEPTION
            'password_reset_tokens has BOTH token_hash and a raw token column. '
            'Raw bearer tokens must never coexist with the canonical hash-only '
            'table. Manual remediation required: back up, then drop the raw token '
            'column (or the whole legacy table) and re-run migration 034/036 so '
            'the canonical schema is authoritative. Raw tokens are NOT migrated.';
    END IF;

    -- Reject: any raw token column present AFTER 034 was recorded. 034 already
    -- owns the canonical hash-only table; 033b should have revoked the legacy
    -- table. A surviving raw token now is an unsafe/mixed state.
    IF v_has_legacy_token AND v_034_recorded THEN
        RAISE EXCEPTION
            'password_reset_tokens still has a raw token column AFTER migration '
            '034_enrollment_control_plane was recorded. Migration 034 owns the '
            'canonical hash-only schema, so the presence of a raw bearer token is '
            'an unsafe/mixed state. Manual remediation required: back up and DROP '
            'the legacy password_reset_tokens table (or the raw token column), '
            'then re-run migration 036 so the canonical schema is verified. Raw '
            'tokens are NOT migrated or reinterpreted.';
    END IF;

    -- At this point we have a table with a raw `token` column and 034 not yet
    -- recorded. Accept it ONLY if it is EXACTLY the known legacy shape.
    SELECT array_agg(column_name ORDER BY column_name) INTO v_cols
    FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'password_reset_tokens';

    SELECT bool_and(c = ANY(v_legacy_cols)) AND count(*) = array_length(v_legacy_cols, 1)
        INTO v_legacy_set_ok
    FROM unnest(v_cols) AS c;

    v_legacy_set_ok := COALESCE(v_legacy_set_ok, false);

    IF v_has_legacy_token AND NOT v_034_recorded AND v_legacy_set_ok THEN
        -- Exactly the known legacy raw-token shape, 034 not yet recorded.
        -- Under the normal lexical runner order (033a -> 033b -> 034) this is
        -- the expected intermediate state: 033a validates the shape and accepts
        -- it, then the IMMEDIATELY-FOLLOWING 033b drops this exact legacy table
        -- (033b case 3), and then 034 creates the canonical hash-only table. No
        -- operator action is required. 033a must NOT drop the table itself,
        -- because 034's CREATE TABLE IF NOT EXISTS would then be a no-op and the
        -- canonical schema would not be built — 033b owns the drop. We emit a
        -- NOTICE (not a WARNING implying failure) so the operator understands the
        -- pending sequence. We never reinterpret tokens here.
        RAISE NOTICE
            'password_reset_tokens is in the exact pre-034 legacy raw-token shape '
            'and migration 034 is not yet recorded. Per the lexical runner order '
            '(033a -> 033b -> 034), the following 033b will drop this legacy table '
            'and then 034 builds the canonical hash-only (token_hash) table. No '
            'operator action is required. Raw bearer tokens are NOT migrated.';
        RETURN;
    END IF;

    -- Reject: unknown/extra raw-token shape (has `token` but is not exactly the
    -- known legacy column set).
    IF v_has_legacy_token AND NOT v_034_recorded AND NOT v_legacy_set_ok THEN
        SELECT count(*) > 0 INTO v_unknown_extra
        FROM unnest(v_cols) AS c
        WHERE c NOT IN (SELECT unnest(v_legacy_cols));
        RAISE EXCEPTION
            'password_reset_tokens has a raw token column but an UNKNOWN/extra '
            'shape (columns: %). Only the exact pre-034 legacy shape (%s) is '
            'acceptable before migration 034 builds the canonical schema. Raw '
            'bearer tokens are NEVER silently reinterpreted or migrated. Manual '
            'remediation required: back up, normalize to the canonical hash-only '
            'schema via a forward corrective migration, then re-run 034/036.',
            v_cols, v_legacy_cols;
    END IF;

    -- Reject: canonical-ish but missing both hashes/token (ambiguous). Treat as
    -- unknown and fail closed.
    RAISE EXCEPTION
        'password_reset_tokens has an unexpected shape (columns: %). It is neither '
        'the canonical hash-only table nor the known legacy raw-token shape. '
        'Migration 033a refuses to proceed. Manual remediation required before '
        'applying migration 034/036.', v_cols;
END
$$;
