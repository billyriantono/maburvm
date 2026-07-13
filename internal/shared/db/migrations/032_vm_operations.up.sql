-- Tracks multi-step VM operations (delete, and later create/rebuild) so the UI
-- can show step-by-step progress and whether the operation actually succeeded.
-- NOTE: vm_id has NO foreign key on purpose — a delete operation must outlive the
-- vms row it removes so the UI can still read the final 'completed' state.
CREATE TABLE IF NOT EXISTS vm_operations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id        UUID NOT NULL,
    operation    VARCHAR(32)  NOT NULL,                    -- delete | create | rebuild
    status       VARCHAR(16)  NOT NULL DEFAULT 'running',  -- running | completed | failed
    current_step INTEGER      NOT NULL DEFAULT 0,
    total_steps  INTEGER      NOT NULL DEFAULT 0,
    step_label   VARCHAR(200) NOT NULL DEFAULT '',
    error        TEXT         NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vm_operations_vm_started ON vm_operations(vm_id, started_at DESC);
