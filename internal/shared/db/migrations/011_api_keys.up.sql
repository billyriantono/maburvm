-- API keys: hashed, per-user credentials for API automation.
CREATE TABLE IF NOT EXISTS api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         VARCHAR(100) NOT NULL,
    key_hash     VARCHAR(64) NOT NULL,
    prefix       VARCHAR(20) NOT NULL,
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash_unique ON api_keys(key_hash) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id) WHERE deleted_at IS NULL;
