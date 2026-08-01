-- Phase 1A Gate-1 remediation 040a is FORWARD-ONLY.
--
-- This down script is intentionally NON-DESTRUCTIVE and FAILS CLOSED. It must
-- never be applied; downgrades are handled by restoring a pre-040a backup or by
-- applying a later forward corrective migration. Reverting 040a would:
--   * remove the reservation lifecycle coherence CHECK and the one-pending-per-VM
--     partial unique index (re-opening the over-count/double-spend risk);
--   * drop the composite (vm_id,user_id) reservation FK and restore the weaker
--     independent FKs (re-opening the ownership-incoherence risk);
--   * revert vms->users / vms->nodes to ON DELETE CASCADE (re-enabling parent
--     deletion erasing VM + disk/storage accounting);
--   * remove vm_disks.lifecycle and the vm_status 'deleting' enum value.
-- We therefore RAISE an actionable error instead of dropping any object; NO
-- destructive SQL is executed.

DO $$
BEGIN
    RAISE EXCEPTION
        'FORWARD-ONLY MIGRATION: 040a_disk_quota_lifecycle_corrections cannot be rolled back. '
        'Do NOT apply this down script. To revert, restore a pre-040a backup or apply a '
        'dedicated forward corrective migration. Destructive rollback is intentionally '
        'disabled to preserve (1) the reservation lifecycle CHECK and one-pending-per-VM '
        'partial unique index, (2) the composite (vm_id,user_id) reservation FK with '
        'ON DELETE CASCADE that guarantees ownership coherence, (3) vms->users and '
        'vms->nodes ON DELETE RESTRICT so parent deletion cannot erase VM/storage '
        'accounting, (4) vm_disks.lifecycle (attached|deleting) and the vm_status '
        'deleting enum value used by the verified-destroy worker lifecycle.'
        USING errcode = 'P0001';
END$$;
