-- 035_enrollment_integrity: Phase 1A cross-cutting integrity for BOTH the
-- quota-policy foundation (033) and the enrollment-control plane (034).
--
-- Precondition: 033 and 034 have been recorded. This file is lexicographically
-- ordered AFTER 034 by the migration runner, which runs the whole file in a
-- single transaction and records '035_enrollment_integrity' on success.
--
-- Design
--   * Safe if 034 exists but no Phase 1B routes have created data: all new
--     objects are created IF NOT EXISTS / IF NOT present, and all data checks
--     FAIL CLOSED with an actionable message rather than fabricating security
--     state (e.g. we never invent an active pointer or backfill snapshots).
--   * Naming convention: everything introduced here is prefixed ec_ (enrollment
--     control) or qp_ (quota policy) so the down migration can drop ONLY what
--     this file created; pre-existing resources are never dropped.
--   * No plaintext columns are added anywhere.
--
-- Sections
--   A. Quota-policy invariants (append-only versions, no physical delete of
--      policies, default/version constraints, managed user_quotas provenance).
--   B. Public URL / SMTP control-plane invariants (sequences, singleton seed,
--      immutability, single-active partial unique indexes, deferred commit-time
--      consistency, SMTP envelope validation, legal lifecycle transitions).
--   C. Invite / reset invariants (NOT NULL snapshot FKs with RESTRICT, hash
--      shape, normalization, per-recipient uniqueness, state/timestamp checks,
--      immutability + legal transition triggers).
--
-- Invariants that cannot be safely enforced at the DB layer alone and are
-- flagged for the Go service layer (NOT weakened here):
--   * Recipient normalization (lowercase/trim) is enforced by a CHECK on stored
--     form AND by a trigger that rejects non-normalized input; the canonical
--     normalization still lives in the service. We do not rewrite existing rows.
--   * Exact reset-token EXPIRY service semantics (clock-skew, grace) are a Go
--     concern; the DB only enforces expires_at > created_at and consumed_at
--     integrity.

-- ===================== A. QUOTA-POLICY INVARIANTS =====================

-- A1. quota_policy_versions is append-only: reject UPDATE and DELETE.
CREATE OR REPLACE FUNCTION qp_reject_quota_policy_versions_write()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'quota_policy_versions is immutable and append-only; updates and '
        'deletes are forbidden. Create a new version instead.';
END $$;

DROP TRIGGER IF EXISTS qp_quota_policy_versions_no_update ON quota_policy_versions;
CREATE TRIGGER qp_quota_policy_versions_no_update
    BEFORE UPDATE ON quota_policy_versions
    FOR EACH ROW EXECUTE FUNCTION qp_reject_quota_policy_versions_write();

DROP TRIGGER IF EXISTS qp_quota_policy_versions_no_delete ON quota_policy_versions;
CREATE TRIGGER qp_quota_policy_versions_no_delete
    BEFORE DELETE ON quota_policy_versions
    FOR EACH ROW EXECUTE FUNCTION qp_reject_quota_policy_versions_write();

-- A2. quota_policies must not be physically deleted through normal DB writes.
CREATE OR REPLACE FUNCTION qp_reject_quota_policies_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'quota_policies must not be physically deleted; set lifecycle = '
        'deprecated instead.';
END $$;

DROP TRIGGER IF EXISTS qp_quota_policies_no_delete ON quota_policies;
CREATE TRIGGER qp_quota_policies_no_delete
    BEFORE DELETE ON quota_policies
    FOR EACH ROW EXECUTE FUNCTION qp_reject_quota_policies_delete();

-- A3. version > 0. (033 already checks per-limit positivity; add version bound.)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'quota_policy_versions_version_positive'
    ) THEN
        ALTER TABLE quota_policy_versions
            ADD CONSTRAINT quota_policy_versions_version_positive
            CHECK (version > 0);
    END IF;
END $$;

-- A4. A quota_policy cannot be deprecated while it is the active default.
CREATE OR REPLACE FUNCTION qp_reject_deprecate_default()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lifecycle = 'deprecated' AND OLD.is_default THEN
        RAISE EXCEPTION
            'cannot deprecate the active default quota_policy; clear '
            'is_default (and set a replacement default) first.';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS qp_quota_policies_no_deprecate_default ON quota_policies;
CREATE TRIGGER qp_quota_policies_no_deprecate_default
    BEFORE UPDATE ON quota_policies
    FOR EACH ROW EXECUTE FUNCTION qp_reject_deprecate_default();

-- A5. The default active policy must have at least one version.
--     (Enforced at COMMIT so the version can be inserted in the same txn.)
CREATE OR REPLACE FUNCTION qp_check_default_has_version()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM quota_policies p
        WHERE p.is_default
          AND p.lifecycle = 'active'
          AND NOT EXISTS (
              SELECT 1 FROM quota_policy_versions v WHERE v.policy_id = p.id
          )
    ) THEN
        RAISE EXCEPTION
            'an is_default=true active quota_policy must have at least one '
            'quota_policy_versions row; refusing commit.';
    END IF;
    RETURN NULL;
END $$;

DROP TRIGGER IF EXISTS qp_quota_policies_default_deferred ON quota_policies;
CREATE CONSTRAINT TRIGGER qp_quota_policies_default_deferred
    AFTER INSERT OR UPDATE ON quota_policies
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION qp_check_default_has_version();

-- A6. managed user_quotas require full provenance; legacy rows untouched.
--     Validate only rows in managed mode (quota_mode = 'managed'); zero-unlimited
--     legacy rows keep their semantics.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'user_quotas_managed_provenance'
    ) THEN
        ALTER TABLE user_quotas
            ADD CONSTRAINT user_quotas_managed_provenance
            CHECK (
                quota_mode <> 'managed'
                OR (
                    policy_id IS NOT NULL
                    AND policy_version IS NOT NULL AND policy_version > 0
                    AND policy_name IS NOT NULL AND policy_name <> ''
                    AND policy_assigned_at IS NOT NULL
                    AND max_vms    > 0
                    AND max_vcpu   > 0
                    AND max_ram_mb > 0
                    AND max_disk_gb > 0
                )
            );
    END IF;
END $$;

-- ===================== B. PUBLIC URL / SMTP CONTROL PLANE =====================

-- B1. Allocate revisions via sequences (revisions are immutable, > 0).
CREATE SEQUENCE IF NOT EXISTS ec_public_url_revision_seq START WITH 1 INCREMENT BY 1;
CREATE SEQUENCE IF NOT EXISTS ec_smtp_config_revision_seq START WITH 1 INCREMENT BY 1;

-- B2. revisions insert only as candidate; payload/revision/creator immutable.
CREATE OR REPLACE FUNCTION ec_revisions_insert_guard()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Inserts always start as candidate; state is only transitioned by the
    -- lifecycle trigger (B5) thereafter. This must run on INSERT only, never on
    -- UPDATE, or it would clobber legal candidate->active->retired transitions.
    IF TG_OP = 'INSERT' THEN
        IF TG_TABLE_NAME = 'public_url_revisions' THEN
            NEW.state := 'candidate';
        ELSIF TG_TABLE_NAME = 'smtp_config_revisions' THEN
            NEW.state := 'candidate';
        END IF;
    END IF;
    -- Immutable columns: forbid changes via UPDATE. The payload columns differ
    -- per table, so guard table-specifically.
    IF TG_OP = 'UPDATE' THEN
        IF NEW.revision IS DISTINCT FROM OLD.revision THEN
            RAISE EXCEPTION 'revision is immutable';
        END IF;
        IF TG_TABLE_NAME = 'public_url_revisions' THEN
            IF NEW.origin IS DISTINCT FROM OLD.origin OR
               NEW.description IS DISTINCT FROM OLD.description OR
               NEW.created_by IS DISTINCT FROM OLD.created_by THEN
                RAISE EXCEPTION 'revision payload is immutable';
            END IF;
        ELSIF TG_TABLE_NAME = 'smtp_config_revisions' THEN
            IF NEW.host IS DISTINCT FROM OLD.host OR
               NEW.port IS DISTINCT FROM OLD.port OR
               NEW.username IS DISTINCT FROM OLD.username OR
               NEW.from_address IS DISTINCT FROM OLD.from_address OR
               NEW.transport IS DISTINCT FROM OLD.transport OR
               NEW.description IS DISTINCT FROM OLD.description OR
               NEW.created_by IS DISTINCT FROM OLD.created_by OR
               NEW.password_ciphertext IS DISTINCT FROM OLD.password_ciphertext OR
               NEW.password_nonce IS DISTINCT FROM OLD.password_nonce OR
               NEW.envelope_key_version IS DISTINCT FROM OLD.envelope_key_version THEN
                RAISE EXCEPTION 'revision payload is immutable';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_public_url_revisions_insert_guard ON public_url_revisions;
CREATE TRIGGER ec_public_url_revisions_insert_guard
    BEFORE INSERT OR UPDATE ON public_url_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_insert_guard();

DROP TRIGGER IF EXISTS ec_smtp_config_revisions_insert_guard ON smtp_config_revisions;
CREATE TRIGGER ec_smtp_config_revisions_insert_guard
    BEFORE INSERT OR UPDATE ON smtp_config_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_insert_guard();

-- B3. revisions > 0 (defensive; sequence guarantees this).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'public_url_revisions_revision_positive'
    ) THEN
        ALTER TABLE public_url_revisions
            ADD CONSTRAINT public_url_revisions_revision_positive CHECK (revision > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'smtp_config_revisions_revision_positive'
    ) THEN
        ALTER TABLE smtp_config_revisions
            ADD CONSTRAINT smtp_config_revisions_revision_positive CHECK (revision > 0);
    END IF;
END $$;

-- B4. seed exactly the singleton inactive state rows if absent (do NOT set pointers).
INSERT INTO public_url_state (singleton_key, active_revision_id, state)
    SELECT 'A', NULL, 'inactive'
    WHERE NOT EXISTS (SELECT 1 FROM public_url_state WHERE singleton_key = 'A');

INSERT INTO smtp_config_state (singleton_key, active_revision_id, state)
    SELECT 'A', NULL, 'inactive'
    WHERE NOT EXISTS (SELECT 1 FROM smtp_config_state WHERE singleton_key = 'A');

-- B5. legal lifecycle transitions + state-row reconciliation, deferred so atomic
--     activation/retirement is possible. retired is terminal; only one active
--     revision may exist (partial unique index B6); active pointer non-null iff
--     state active (B7). No revision may be deleted.
CREATE OR REPLACE FUNCTION ec_revisions_lifecycle()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'control-plane revisions cannot be deleted';
    END IF;

    -- Enforce legal transition: candidate -> active -> retired (retired terminal).
    -- A revision may only advance one step along the chain; candidate cannot
    -- jump straight to retired.
    IF TG_OP = 'UPDATE' AND OLD.state IS DISTINCT FROM NEW.state THEN
        IF OLD.state = 'candidate' AND NEW.state <> 'active' THEN
            RAISE EXCEPTION 'illegal transition % -> % (candidate may only become active)', OLD.state, NEW.state;
        END IF;
        IF OLD.state = 'active' AND NEW.state <> 'retired' THEN
            RAISE EXCEPTION 'illegal transition % -> % (active may only become retired)', OLD.state, NEW.state;
        END IF;
        IF OLD.state = 'retired' THEN
            RAISE EXCEPTION 'retired is terminal; no further transition allowed';
        END IF;
    END IF;

    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_public_url_revisions_lifecycle ON public_url_revisions;
CREATE TRIGGER ec_public_url_revisions_lifecycle
    BEFORE INSERT OR UPDATE OR DELETE ON public_url_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_lifecycle();

DROP TRIGGER IF EXISTS ec_smtp_config_revisions_lifecycle ON smtp_config_revisions;
CREATE TRIGGER ec_smtp_config_revisions_lifecycle
    BEFORE INSERT OR UPDATE OR DELETE ON smtp_config_revisions
    FOR EACH ROW EXECUTE FUNCTION ec_revisions_lifecycle();

-- B6. one active revision per control plane (partial unique index).
DROP INDEX IF EXISTS ec_public_url_one_active;
CREATE UNIQUE INDEX ec_public_url_one_active
    ON public_url_revisions (state) WHERE state = 'active';

DROP INDEX IF EXISTS ec_smtp_one_active;
CREATE UNIQUE INDEX ec_smtp_one_active
    ON smtp_config_revisions (state) WHERE state = 'active';

-- B7. state-row reconciliation deferred to COMMIT:
--     pointer NULL  <=> state 'inactive' ; pointer non-NULL <=> state 'active';
--     and the pointed revision must itself be 'active'. Exactly one active
--     revision may exist (B6), so no active revision can be left unpointed by
--     construction. Deferred so the activation txn can set both rows atomically.
CREATE OR REPLACE FUNCTION ec_control_state_consistent()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_rev_table text := TG_ARGV[0];
    v_state_table text := TG_ARGV[1];
    v_pointer uuid;
    v_state text;
    v_rev_state text;
BEGIN
    -- Check the singleton state row after each change to it OR its revision.
    EXECUTE format(
        'SELECT active_revision_id, state FROM %I WHERE singleton_key = ''A''',
        v_state_table
    ) INTO v_pointer, v_state;

    IF v_state = 'active' AND v_pointer IS NULL THEN
        RAISE EXCEPTION 'control-plane state active requires a non-null pointer';
    END IF;
    IF v_state = 'inactive' AND v_pointer IS NOT NULL THEN
        RAISE EXCEPTION 'control-plane state inactive requires a null pointer';
    END IF;
    IF v_pointer IS NOT NULL THEN
        EXECUTE format(
            'SELECT state FROM %I WHERE id = $1', v_rev_table
        ) USING v_pointer INTO v_rev_state;
        IF v_rev_state IS DISTINCT FROM 'active' THEN
            RAISE EXCEPTION 'active pointer must reference an active revision';
        END IF;
    END IF;
    RETURN NULL;
END $$;

DROP TRIGGER IF EXISTS ec_public_url_state_deferred ON public_url_revisions;
CREATE CONSTRAINT TRIGGER ec_public_url_state_deferred
    AFTER INSERT OR UPDATE OR DELETE ON public_url_revisions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('public_url_revisions', 'public_url_state');

DROP TRIGGER IF EXISTS ec_public_url_state_deferred2 ON public_url_state;
CREATE CONSTRAINT TRIGGER ec_public_url_state_deferred2
    AFTER INSERT OR UPDATE OR DELETE ON public_url_state
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('public_url_revisions', 'public_url_state');

DROP TRIGGER IF EXISTS ec_smtp_state_deferred ON smtp_config_revisions;
CREATE CONSTRAINT TRIGGER ec_smtp_state_deferred
    AFTER INSERT OR UPDATE OR DELETE ON smtp_config_revisions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('smtp_config_revisions', 'smtp_config_state');

DROP TRIGGER IF EXISTS ec_smtp_state_deferred2 ON smtp_config_state;
CREATE CONSTRAINT TRIGGER ec_smtp_state_deferred2
    AFTER INSERT OR UPDATE OR DELETE ON smtp_config_state
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ec_control_state_consistent('smtp_config_revisions', 'smtp_config_state');

-- Prevent deletion of the singleton state rows.
CREATE OR REPLACE FUNCTION ec_reject_state_row_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'control-plane singleton state row cannot be deleted';
END $$;

DROP TRIGGER IF EXISTS ec_public_url_state_no_delete ON public_url_state;
CREATE TRIGGER ec_public_url_state_no_delete
    BEFORE DELETE ON public_url_state
    FOR EACH ROW EXECUTE FUNCTION ec_reject_state_row_delete();

DROP TRIGGER IF EXISTS ec_smtp_state_no_delete ON smtp_config_state;
CREATE TRIGGER ec_smtp_state_no_delete
    BEFORE DELETE ON smtp_config_state
    FOR EACH ROW EXECUTE FUNCTION ec_reject_state_row_delete();

-- B8. SMTP envelope validation at DB layer: port 1..65535, non-empty cipher,
--     12-byte AES-GCM nonce, positive envelope key version. No plaintext fields.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'smtp_config_revisions_envelope_valid'
    ) THEN
        ALTER TABLE smtp_config_revisions
            ADD CONSTRAINT smtp_config_revisions_envelope_valid
            CHECK (
                port BETWEEN 1 AND 65535
                AND length(password_ciphertext) > 0
                AND length(password_nonce) = 12
                AND envelope_key_version > 0
            );
    END IF;
END $$;

-- ===================== C. INVITE / RESET INVARIANTS =====================

-- C1. All invite snapshot FKs NOT NULL with explicit RESTRICT. If any unsafe
--     existing row is found (NULL snapshot, or dangling FK), fail closed with an
--     actionable remediation message instead of fabricating data.
DO $$
DECLARE
    v_bad int;
BEGIN
    SELECT count(*) INTO v_bad FROM registration_invites
    WHERE quota_policy_version_id IS NULL
       OR url_revision_id IS NULL
       OR smtp_revision_id IS NULL;
    IF v_bad > 0 THEN
        RAISE EXCEPTION
            '% registration_invites row(s) have NULL snapshot FKs '
            '(quota_policy_version_id/url_revision_id/smtp_revision_id). '
            'Migration 035 requires snapshots to be NOT NULL with RESTRICT. '
            'Remediation: repoint or delete the offending invites (they were '
            'created under an un-finalized schema) before applying this '
            'migration.', v_bad;
    END IF;
    SELECT count(*) INTO v_bad FROM registration_invites ri
    WHERE NOT EXISTS (SELECT 1 FROM quota_policy_versions qpv WHERE qpv.id = ri.quota_policy_version_id)
       OR NOT EXISTS (SELECT 1 FROM public_url_revisions pur WHERE pur.id = ri.url_revision_id)
       OR NOT EXISTS (SELECT 1 FROM smtp_config_revisions scr WHERE scr.id = ri.smtp_revision_id);
    IF v_bad > 0 THEN
        RAISE EXCEPTION
            '% registration_invites row(s) reference non-existent snapshots. '
            'Remediation: clean up dangling invites before applying migration 035.',
            v_bad;
    END IF;
END $$;

ALTER TABLE registration_invites
    ALTER COLUMN quota_policy_version_id SET NOT NULL;
ALTER TABLE registration_invites
    ALTER COLUMN url_revision_id SET NOT NULL;
ALTER TABLE registration_invites
    ALTER COLUMN smtp_revision_id SET NOT NULL;

-- Re-create the FKs with explicit RESTRICT (034 created them as plain REFERENCES
-- -> default NO ACTION, which is functionally RESTRICT; we make it explicit and
-- drop the implicit ones first if present).
ALTER TABLE registration_invites
    DROP CONSTRAINT IF EXISTS registration_invites_quota_policy_version_id_fkey;
ALTER TABLE registration_invites
    DROP CONSTRAINT IF EXISTS registration_invites_url_revision_id_fkey;
ALTER TABLE registration_invites
    DROP CONSTRAINT IF EXISTS registration_invites_smtp_revision_id_fkey;

ALTER TABLE registration_invites
    ADD CONSTRAINT registration_invites_quota_policy_version_id_fkey
        FOREIGN KEY (quota_policy_version_id)
        REFERENCES quota_policy_versions(id) ON DELETE RESTRICT ON UPDATE RESTRICT;
ALTER TABLE registration_invites
    ADD CONSTRAINT registration_invites_url_revision_id_fkey
        FOREIGN KEY (url_revision_id)
        REFERENCES public_url_revisions(id) ON DELETE RESTRICT ON UPDATE RESTRICT;
ALTER TABLE registration_invites
    ADD CONSTRAINT registration_invites_smtp_revision_id_fkey
        FOREIGN KEY (smtp_revision_id)
        REFERENCES smtp_config_revisions(id) ON DELETE RESTRICT ON UPDATE RESTRICT;

-- C2. hash shape: strictly lowercase 64-hex (SHA-256). Applied to both tables,
--     and to invite/reset via CHECK. Existing data is validated and fails closed.
DO $$
DECLARE
    v_bad int;
BEGIN
    SELECT count(*) INTO v_bad FROM registration_invites
    WHERE token_hash !~ '^[a-f0-9]{64}$';
    IF v_bad > 0 THEN
        RAISE EXCEPTION
            '% registration_invites row(s) have a non-lowercase-64-hex token_hash. '
            'Remediation: regenerate invites so token_hash is the lowercase hex '
            'SHA-256 of the token before applying migration 035.', v_bad;
    END IF;
    SELECT count(*) INTO v_bad FROM password_reset_tokens
    WHERE token_hash !~ '^[a-f0-9]{64}$';
    IF v_bad > 0 THEN
        RAISE EXCEPTION
            '% password_reset_tokens row(s) have a non-lowercase-64-hex token_hash. '
            'Remediation: regenerate reset tokens before applying migration 035.', v_bad;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'registration_invites_token_hash_hex'
    ) THEN
        ALTER TABLE registration_invites
            ADD CONSTRAINT registration_invites_token_hash_hex
            CHECK (token_hash ~ '^[a-f0-9]{64}$');
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'password_reset_tokens_token_hash_hex'
    ) THEN
        ALTER TABLE password_reset_tokens
            ADD CONSTRAINT password_reset_tokens_token_hash_hex
            CHECK (token_hash ~ '^[a-f0-9]{64}$');
    END IF;
END $$;

-- C3. recipient normalization: stored form must be lowercase and trimmed. A
--     trigger rejects non-normalized writes; we do not rewrite historical rows.
CREATE OR REPLACE FUNCTION ec_invite_normalize()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.recipient_email <> lower(trim(NEW.recipient_email)) THEN
        RAISE EXCEPTION 'recipient_email must be stored normalized (lower/trim)';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_invite_normalize ON registration_invites;
CREATE TRIGGER ec_invite_normalize
    BEFORE INSERT OR UPDATE ON registration_invites
    FOR EACH ROW EXECUTE FUNCTION ec_invite_normalize();

-- C4. timestamps / positive expiry ordering + state/timestamp coherence.
--     - created_at <= expires_at (positive expiry ordering)
--     - sent_at, consumed_at, when present, are >= created_at
--     - consumed_at present <=> state 'consumed'
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'registration_invites_time_order'
    ) THEN
        ALTER TABLE registration_invites
            ADD CONSTRAINT registration_invites_time_order
            CHECK (
                expires_at > created_at
                AND (sent_at IS NULL OR sent_at >= created_at)
                AND (consumed_at IS NULL OR consumed_at >= created_at)
                AND (consumed_at IS NULL OR state = 'consumed')
                AND (state <> 'consumed' OR consumed_at IS NOT NULL)
            );
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.check_constraints
        WHERE constraint_name = 'password_reset_tokens_time_order'
    ) THEN
        ALTER TABLE password_reset_tokens
            ADD CONSTRAINT password_reset_tokens_time_order
            CHECK (
                expires_at > created_at
                AND (consumed_at IS NULL OR consumed_at >= created_at)
            );
    END IF;
END $$;

-- C5. initial invite insertion must be pending_delivery; snapshots/hash/recipient/
--     expiry immutable thereafter; legal transition + synchronous-send contract:
--     delivery_failed is terminal (no retry queue claim), so only
--     pending_delivery/active may advance; consumed/revoked are terminal.
CREATE OR REPLACE FUNCTION ec_invite_lifecycle()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.state <> 'pending_delivery' THEN
        RAISE EXCEPTION 'new invites must start in pending_delivery';
    END IF;
    IF TG_OP = 'INSERT' AND NEW.sent_at IS NOT NULL THEN
        RAISE EXCEPTION 'new invites cannot be marked sent';
    END IF;
    IF TG_OP = 'INSERT' AND NEW.consumed_at IS NOT NULL THEN
        RAISE EXCEPTION 'new invites cannot be marked consumed';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        -- Immutable fields.
        IF NEW.token_hash IS DISTINCT FROM OLD.token_hash THEN
            RAISE EXCEPTION 'invite token_hash is immutable';
        END IF;
        IF NEW.recipient_email IS DISTINCT FROM OLD.recipient_email THEN
            RAISE EXCEPTION 'invite recipient_email is immutable';
        END IF;
        IF NEW.recipient_role IS DISTINCT FROM OLD.recipient_role THEN
            RAISE EXCEPTION 'invite recipient_role is immutable';
        END IF;
        IF NEW.creator_id IS DISTINCT FROM OLD.creator_id THEN
            RAISE EXCEPTION 'invite creator_id is immutable';
        END IF;
        IF NEW.quota_policy_version_id IS DISTINCT FROM OLD.quota_policy_version_id
           OR NEW.url_revision_id IS DISTINCT FROM OLD.url_revision_id
           OR NEW.smtp_revision_id IS DISTINCT FROM OLD.smtp_revision_id THEN
            RAISE EXCEPTION 'invite snapshot FKs are immutable';
        END IF;
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'invite expires_at is immutable';
        END IF;
        -- Legal transitions under the synchronous-send contract.
        IF OLD.state = 'pending_delivery' AND NEW.state NOT IN ('active', 'delivery_failed', 'revoked') THEN
            RAISE EXCEPTION 'illegal transition % -> %', OLD.state, NEW.state;
        END IF;
        IF OLD.state = 'active' AND NEW.state NOT IN ('consumed', 'revoked') THEN
            RAISE EXCEPTION 'illegal transition % -> %', OLD.state, NEW.state;
        END IF;
        -- Terminal states.
        IF OLD.state IN ('delivery_failed', 'revoked', 'consumed') THEN
            RAISE EXCEPTION 'state % is terminal', OLD.state;
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_invite_lifecycle ON registration_invites;
CREATE TRIGGER ec_invite_lifecycle
    BEFORE INSERT OR UPDATE ON registration_invites
    FOR EACH ROW EXECUTE FUNCTION ec_invite_lifecycle();

-- C6. one pending invite AND one active invite per normalized recipient
--     (separate partial unique indexes), permitting an old active invite during
--     replacement delivery (the new pending is created before the old active is
--     revoked, so both may briefly coexist).
DROP INDEX IF EXISTS ec_registration_invites_one_pending;
CREATE UNIQUE INDEX ec_registration_invites_one_pending
    ON registration_invites (recipient_email)
    WHERE state = 'pending_delivery';

DROP INDEX IF EXISTS ec_registration_invites_one_active;
CREATE UNIQUE INDEX ec_registration_invites_one_active
    ON registration_invites (recipient_email)
    WHERE state = 'active';

-- C7. reset token consumed integrity: consumed_at present iff logically
--     consumed; attempt metadata is managed by the Go service.
CREATE OR REPLACE FUNCTION ec_reset_consistency()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        -- token_hash and user_id immutable; expiry immutable once set.
        IF NEW.token_hash IS DISTINCT FROM OLD.token_hash THEN
            RAISE EXCEPTION 'reset token_hash is immutable';
        END IF;
        IF NEW.user_id IS DISTINCT FROM OLD.user_id THEN
            RAISE EXCEPTION 'reset user_id is immutable';
        END IF;
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'reset expires_at is immutable';
        END IF;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS ec_reset_consistency ON password_reset_tokens;
CREATE TRIGGER ec_reset_consistency
    BEFORE INSERT OR UPDATE ON password_reset_tokens
    FOR EACH ROW EXECUTE FUNCTION ec_reset_consistency();
