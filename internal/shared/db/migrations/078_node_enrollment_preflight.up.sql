-- Single-use enrollment tokens for the node installer, created in the Add node
-- flow. Only a SHA-256 of the token is kept; the token is shown once.
CREATE TABLE IF NOT EXISTS node_enrollments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    token_hash VARCHAR(64) NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    used_from VARCHAR(64) NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_node_enrollments_token ON node_enrollments (token_hash);
CREATE INDEX IF NOT EXISTS idx_node_enrollments_node ON node_enrollments (node_id);

-- The node's last preflight report as its agent returned it (JSON), and when.
-- Written and read by table name, so a full-row save of a node cannot clear it.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS preflight TEXT;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS preflight_at TIMESTAMPTZ;
