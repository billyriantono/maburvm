-- A staff account's role: a built-in key (administrator, support, billing,
-- readonly) or the id of a row in staff_roles. NULL is Administrator, so every
-- staff account that exists today keeps full access. The founding
-- administrator is the Owner whatever this says.
ALTER TABLE users ADD COLUMN IF NOT EXISTS staff_role VARCHAR(64);

-- Custom staff roles, made of permissions from the panel's catalog.
CREATE TABLE IF NOT EXISTS staff_roles (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_staff_roles_name ON staff_roles (LOWER(name));

-- One row per sign-in. The access token carries the id, and a revoked row
-- ends the session at its next request. The old sessions table was never used.
CREATE TABLE IF NOT EXISTS user_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    method VARCHAR(20) NOT NULL,
    ip_address VARCHAR(64) NOT NULL DEFAULT '',
    user_agent VARCHAR(500) NOT NULL DEFAULT '',
    impersonator_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions (user_id, created_at DESC);

-- Every sign-in attempt, good or bad, and password reset requests: the
-- account's sign-in history, and what the sign-in captcha counts.
CREATE TABLE IF NOT EXISTS sign_in_events (
    id UUID PRIMARY KEY,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL DEFAULT '',
    method VARCHAR(20) NOT NULL,
    success BOOLEAN NOT NULL,
    note VARCHAR(255) NOT NULL DEFAULT '',
    ip_address VARCHAR(64) NOT NULL DEFAULT '',
    user_agent VARCHAR(500) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_sign_in_events_user ON sign_in_events (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sign_in_events_ip ON sign_in_events (ip_address, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sign_in_events_email ON sign_in_events (LOWER(email), created_at DESC);
