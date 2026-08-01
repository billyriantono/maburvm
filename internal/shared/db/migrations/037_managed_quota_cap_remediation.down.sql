-- Phase 1A Gate-1 migration 037 is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. Removing
-- the managed-quota + platform-cap structure via a destructive rollback would
-- orphan managed user_quotas provenance, drop the active-cap pointer, and
-- silently revert the user-level legacy/managed marker -- violating the
-- integrity contract. Downgrades must be performed by restoring a pre-037
-- backup or by applying a forward corrective migration, never by reverting 037.
--
-- We therefore RAISE an actionable error directing operators to the supported
-- remediation path instead of dropping anything.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 037_managed_quota_cap_remediation cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-037 backup or apply a '
        'dedicated forward corrective migration. Destructive rollback is intentionally '
        'disabled to preserve managed user_quotas provenance and the active platform-cap pointer.'
        USING errcode = 'P0001';
END$$;
