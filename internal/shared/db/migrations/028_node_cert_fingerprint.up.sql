-- Store each node agent's pinned TLS certificate fingerprint (SHA-256 hex of the
-- leaf cert). The panel records it on first connection (trust on first use) and
-- verifies it on every later connection, so a man-in-the-middle on the panel↔node
-- network is rejected even though agents use self-signed certificates. Empty =
-- not yet pinned (next connection will record it). Clear this column to re-trust
-- a legitimately re-provisioned node.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS cert_fingerprint VARCHAR(128) NOT NULL DEFAULT '';
