-- The root password the panel generated for a new or reinstalled VM, kept
-- encrypted for a short time so the owner can read it after signing in. It is
-- never put in an email. Rows are erased when they expire.
CREATE TABLE IF NOT EXISTS vm_initial_secrets (
    vm_id      uuid PRIMARY KEY REFERENCES vms(id) ON DELETE CASCADE,
    ciphertext text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vm_initial_secrets_expires ON vm_initial_secrets (expires_at);
