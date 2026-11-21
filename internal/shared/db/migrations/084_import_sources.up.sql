-- A panel the operator is moving from: where its admin API answers and the
-- credentials for it (encrypted by the panel). snapshot is the last data read
-- from it and from the nodes, the one an import applies; options are the
-- mapping choices made with that preview. tls_fingerprint pins the SHA-256 of
-- a self-signed certificate the operator chose to trust.
CREATE TABLE IF NOT EXISTS import_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind VARCHAR(20) NOT NULL DEFAULT 'virtualizor',
    name VARCHAR(100) NOT NULL,
    base_url VARCHAR(500) NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',
    api_pass TEXT NOT NULL DEFAULT '',
    tls_fingerprint VARCHAR(100) NOT NULL DEFAULT '',
    options JSONB NOT NULL DEFAULT '{}',
    snapshot JSONB,
    snapshot_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- What each record of a source became here. Re-running an import skips what
-- is linked. created marks an object the import made; invited_at is when an
-- account it created was sent its invitation.
CREATE TABLE IF NOT EXISTS import_links (
    source_id UUID NOT NULL REFERENCES import_sources(id) ON DELETE CASCADE,
    kind VARCHAR(8) NOT NULL,
    source_key VARCHAR(64) NOT NULL,
    target_id UUID NOT NULL,
    created BOOLEAN NOT NULL DEFAULT FALSE,
    invited_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, kind, source_key)
);
CREATE INDEX IF NOT EXISTS idx_import_links_target ON import_links (kind, target_id);
