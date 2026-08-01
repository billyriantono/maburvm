-- 038_invite_consumption_lifecycle_alignment down migration: intentionally
-- FAILS CLOSED (P0001 only).
--
-- Migration 038 only RE-PLACES the ec_invite_lifecycle guard to permit the exact
-- active -> consumed transition (correcting 036's blocker where consumed_at was
-- unconditionally immutable). It does not create new tables or data that need
-- dropping. Reversing it would re-introduce the Oracle blocker: ConsumeInviteTx
-- could no longer mark an invite consumed, breaking Phase 1B enrollment.
-- Forward-only policy: do NOT apply this down script.
--
-- To reverse 038, restore from a pre-migration backup or apply a forward
-- corrective migration. Never run this down script against a live database.

DO $$
BEGIN
    RAISE EXCEPTION
        'Migration 038_invite_consumption_lifecycle_alignment is forward-only and '
        'cannot be reversed. It corrects the invite consumption lifecycle so '
        'ConsumeInviteTx can perform the legal active -> consumed transition. '
        'Reversing it would re-introduce the blocker that prevents invite '
        'consumption. To "undo" its effect, restore from a pre-migration backup '
        'or apply a forward corrective migration. Never apply this down script '
        'against a live database.';
END $$;
