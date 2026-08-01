-- 033a_reset_shape_guard down migration: intentionally FAILS CLOSED.
--
-- Migration 033a is a read-only guard. It neither creates, drops, nor modifies
-- any relation or data; it only inspects the `password_reset_tokens` shape and
-- refuses unsafe/mixed states. There is therefore nothing reversible here.
--
-- Forward-only policy: do NOT apply this down script. If you need to reverse the
-- state that 033a validated, perform a backup restore or a forward corrective
-- migration instead. Re-applying 033a is idempotent and safe (it re-validates
-- the same invariants). Dropping the recorded version without a restore would
-- leave the database in whatever shape 033a found, which may be unsafe.

DO $$
BEGIN
    RAISE EXCEPTION
        'Migration 033a_reset_shape_guard is forward-only and cannot be reversed. '
        'It performs no structural change (it only validates that '
        'password_reset_tokens is in an acceptable shape). To "undo" its effect, '
        'restore from a pre-migration backup or apply a forward corrective '
        'migration. Never apply this down script against a live database.';
END $$;
