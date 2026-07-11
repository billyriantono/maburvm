-- Persistent idempotency store for the billing webhook. Previously the handler
-- kept processed request results in an in-memory map, which was lost on restart
-- (so a billing system retrying a webhook after a panel restart could re-process
-- it — e.g. provision a second VM). Persisting the result keyed by the caller's
-- idempotency key makes replays return the original response across restarts.
CREATE TABLE IF NOT EXISTS billing_idempotency (
    idempotency_key VARCHAR(255) PRIMARY KEY,
    request_id      UUID        NOT NULL,
    event           VARCHAR(64) NOT NULL DEFAULT '',
    response        JSONB       NOT NULL,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

-- Lets the periodic cleanup drop expired rows efficiently.
CREATE INDEX IF NOT EXISTS idx_billing_idempotency_expires ON billing_idempotency(expires_at);
