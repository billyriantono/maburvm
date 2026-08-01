-- Phase 1A Gate-1 correction 039 is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. Migration
-- 039 hardens managed-snapshot data integrity: it adds a row trigger that
-- requires a managed snapshot to exactly equal its referenced immutable
-- quota_policy_versions, an actionable migration preflight that rejects extant
-- managed-user drift, and DEFERRABLE INITIALLY DEFERRED cross-table coherence
-- checks between users.quota_mode (authoritative) and user_quotas. Reversing it
-- would silently re-open the tamper/coherence contract and re-admit managed
-- users with legacy/multiple/mismatched snapshots, so destructive rollback is
-- intentionally disabled.
--
-- Downgrades must be performed by restoring a pre-039 backup or by applying a
-- later forward corrective migration, never by reverting 039. We therefore RAISE
-- an actionable error directing operators to the supported remediation path
-- instead of dropping anything.
--
-- NOTE: a backward corrective migration that wished to remove 039's objects must
-- DROP the two constraint triggers, the two helper functions, and the row
-- trigger (in appropriate order) and is out of scope for this fail-closed
-- down script.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 039_managed_quota_snapshot_integrity cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-039 backup or apply a '
        'dedicated forward corrective migration. Destructive rollback is intentionally '
        'disabled to preserve the managed-snapshot data-integrity contract (exact-match '
        'row trigger against the immutable quota_policy_versions, drift preflight, and '
        'DEFERRABLE INITIALLY DEFERRED cross-table coherence between users.quota_mode and '
        'user_quotas).'
        USING errcode = 'P0001';
END$$;
