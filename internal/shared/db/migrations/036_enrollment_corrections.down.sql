-- 036_enrollment_corrections down migration: intentionally FAILS CLOSED.
--
-- Migration 036 only RE-DEFINES guarding functions (CREATE OR REPLACE) and ADDS
-- backstop CHECK constraints / corrections on top of 033-035. It does not create
-- any new table or data that would need dropping, and reversing it would mean
-- reverting the control-plane invariants to their weaker 035 state -- which is
-- unsafe (it would reintroduce the SMTP immutability bug and the orphan-active
-- hole). Forward-only policy: do NOT apply this down script.
--
-- To reverse the corrections applied by 036, restore from a pre-migration backup
-- or apply a forward corrective migration. Never run this down script against a
-- live database; doing so would leave the enrollment control plane in a weaker,
-- bug-prone invariant state and could desynchronize schema_migrations.

DO $$
BEGIN
    RAISE EXCEPTION
        'Migration 036_enrollment_corrections is forward-only and cannot be '
        'reversed. It strengthens the enrollment control-plane invariants '
        '(SMTP immutability fix, singleton/active coherence, reset + invite '
        'lifecycle backstops) on top of migrations 033-035. Reversing it would '
        'reintroduce known defects. To "undo" its effect, restore from a '
        'pre-migration backup or apply a forward corrective migration. Never '
        'apply this down script against a live database.';
END $$;
