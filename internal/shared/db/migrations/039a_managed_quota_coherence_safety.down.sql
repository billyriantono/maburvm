-- Phase 1A Gate-1 remediation 039a is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. It must
-- never be applied; downgrades are handled by restoring a pre-039a backup or by
-- applying a later forward corrective migration. Reverting 039a would restore
-- the broken DELETE behavior on user_quotas (every delete aborts because the
-- coherence trigger referenced the unassigned NEW record), re-breaking the
-- managed zero-row pending-state transition and legacy quota deletions. We
-- therefore RAISE an actionable error instead of dropping any object; NO
-- destructive SQL is executed.
--
-- NOTE: a backward corrective migration that wished to remove 039a's objects
-- would DROP the corrected function (and, if reverting 039 entirely, the related
-- coherence objects) and is out of scope for this fail-closed down script.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 039a_managed_quota_coherence_safety cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-039a backup or apply a '
        'dedicated forward corrective migration. Destructive rollback is intentionally '
        'disabled to preserve the corrected deferred coherence trigger '
        '(trg_user_quotas_managed_coherence) which permits DELETE on user_quotas for both '
        'managed pending-state transitions (zero-row legal state) and legacy quota deletions.'
        USING errcode = 'P0001';
END$$;
