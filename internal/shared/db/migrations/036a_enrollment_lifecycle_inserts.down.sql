-- 036a_enrollment_lifecycle_inserts down migration: intentionally FAILS CLOSED.
--
-- Migration 036a only RE-DEFINES the ec_reset_consistency guard (adding the
-- INSERT invariants for unconsumed/zero-attempt reset tokens and the
-- last_attempt_at coherence rule) and ADDS the registration_invites_pending_contract
-- CHECK on top of 033-036. It does not create new tables or data that need
-- dropping, and reversing it would weaken the enrollment control-plane invariant
-- set (reintroducing the "reset may be inserted already-consumed / with stale
-- attempts" and "pending invite may carry delivery markers" holes). Forward-only
-- policy: do NOT apply this down script.
--
-- To reverse 036a, restore from a pre-migration backup or apply a forward
-- corrective migration. Never run this down script against a live database.

DO $$
BEGIN
    RAISE EXCEPTION
        'Migration 036a_enrollment_lifecycle_inserts is forward-only and cannot '
        'be reversed. It strengthens reset-token INSERT invariants and the pending '
        'invite structural contract on top of migrations 033-036. Reversing it '
        'would reintroduce known lifecycle gaps. To "undo" its effect, restore '
        'from a pre-migration backup or apply a forward corrective migration. '
        'Never apply this down script against a live database.';
END $$;
