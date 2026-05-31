-- System settings: one JSON row per settings section (general, security,
-- backup, api, email). Admin-managed via the panel Settings → System page.
CREATE TABLE IF NOT EXISTS system_settings (
    section    VARCHAR(64) PRIMARY KEY,
    data       JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
