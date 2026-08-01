-- At most one active (pending or in_progress) backup per VM. Enforces the
-- concurrent-backup guard atomically at the DB layer, covering both the manual
-- (CreateBackup) and scheduled paths and closing the TOCTOU race a SELECT-then-
-- INSERT app check cannot. Mirrors the partial-unique-index precedent from 004.

-- Pre-dedupe: if the live DB already has >1 active backup for a VM, keep the
-- newest and fail the rest, so the unique index can be created. (No error_message
-- write — that column is absent on drifted live DBs.)
UPDATE backups SET status = 'failed'
WHERE status IN ('pending', 'in_progress') AND deleted_at IS NULL
  AND id NOT IN (
    SELECT DISTINCT ON (vm_id) id FROM backups
    WHERE status IN ('pending', 'in_progress') AND deleted_at IS NULL
    ORDER BY vm_id, created_at DESC
  );

CREATE UNIQUE INDEX IF NOT EXISTS ux_backups_active_per_vm
    ON backups (vm_id)
    WHERE status IN ('pending', 'in_progress') AND deleted_at IS NULL;
