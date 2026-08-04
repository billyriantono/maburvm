-- Per-user SSH public keys, selectable when creating or rebuilding a VM
-- (parity). Only public keys are stored.
CREATE TABLE IF NOT EXISTS ssh_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    public_key  TEXT NOT NULL,
    fingerprint VARCHAR(128) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ssh_keys_user ON ssh_keys(user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ssh_keys_deleted_at ON ssh_keys(deleted_at);
-- A user cannot register the same key twice (by fingerprint).
CREATE UNIQUE INDEX IF NOT EXISTS uq_ssh_keys_user_fingerprint ON ssh_keys(user_id, fingerprint) WHERE deleted_at IS NULL;
