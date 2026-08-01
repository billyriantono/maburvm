-- Phase 1 Gate-1 correction 037b is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. Migration
-- 037b replaces ONLY the platform-cap revision-immutability trigger
-- (trg_platform_cap_revision_immutable) to align the enforced lifecycle with the
-- QuotaPolicyRepository contract (candidate->retired withdrawal, active->retired
-- withdrawal, immutable activation timestamps, retired-only terminal state).
-- Reversing it would drop the corrected lifecycle guard and silently re-open the
-- mismatch that breaks RetireCapRevision for stale candidates. Downgrades must be
-- performed by restoring a pre-037b backup or by applying a later forward
-- corrective migration, never by reverting 037b.
--
-- We therefore RAISE an actionable error directing operators to the supported
-- remediation path instead of dropping anything.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 037b_platform_cap_lifecycle_alignment cannot be '
        'rolled back. Do NOT apply this down script. To revert, restore a pre-037b '
        'backup or apply a dedicated forward corrective migration. Destructive '
        'rollback is intentionally disabled to preserve the corrected platform-cap '
        'lifecycle contract (candidate->retired withdrawal, active->retired '
        'withdrawal, immutable activation timestamps, and retired-only terminal '
        'state) required by RetireCapRevision.'
        USING errcode = 'P0001';
END$$;
