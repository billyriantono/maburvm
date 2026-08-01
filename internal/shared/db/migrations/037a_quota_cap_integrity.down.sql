-- Phase 1A Gate-1 correction 037a is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. Migration
-- 037a hardens quota-cap integrity (orphan preflight, policy-cap INSERT-only
-- guard, managed-snapshot cap provenance, platform cap control-plane triggers).
-- Reversing it would drop the deferred coherence, revision immutability, state
-- row protection, and managed-cap guards, silently re-opening the integrity
-- contract. Downgrades must be performed by restoring a pre-037a backup or by
-- applying a later forward corrective migration, never by reverting 037a.
--
-- We therefore RAISE an actionable error directing operators to the supported
-- remediation path instead of dropping anything.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 037a_quota_cap_integrity cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-037a backup or '
        'apply a dedicated forward corrective migration. Destructive rollback is '
        'intentionally disabled to preserve the quota-cap integrity contract '
        '(deferred coherence, revision immutability, state-row protection, and '
        'managed-snapshot cap provenance).'
        USING errcode = 'P0001';
END$$;
