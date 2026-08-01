-- Phase 1A Gate-1 remediation 040 is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. It must
-- never be applied; downgrades are handled by restoring a pre-040 backup or by
-- applying a later forward corrective migration. Reverting 040 would drop the
-- nonnegative CHECK on user_quotas (re-opening the negative-means-unlimited
-- hazard) and drop the disk_quota_reservations table (breaking the pending
-- extra-disk admission lifecycle). We therefore RAISE an actionable error
-- instead of dropping any object; NO destructive SQL is executed.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 040_disk_quota_reservation cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-040 backup or '
        'apply a dedicated forward corrective migration. Destructive rollback is '
        'intentionally disabled to preserve (1) the nonnegative CHECK on '
        'user_quotas (max_vms/max_vcpu/max_ram_mb/max_disk_gb >= 0), which prevents '
        'negative limits from ever being interpreted as unlimited, and (2) the '
        'disk_quota_reservations table that backs the pending extra-disk admission '
        'lifecycle (reserve -> agent AttachDisk -> consume or release).'
        USING errcode = 'P0001';
END$$;
