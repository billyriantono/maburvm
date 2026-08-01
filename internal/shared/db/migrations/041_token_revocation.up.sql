-- Per-user token revocation cutoff. RequireAuth rejects any access JWT whose iat
-- predates this timestamp, giving logout a real server-side revocation for the
-- otherwise-stateless 24h JWT. NULL = nothing revoked (default for all users).
ALTER TABLE users ADD COLUMN IF NOT EXISTS token_revoked_at TIMESTAMPTZ;
