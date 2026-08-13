-- Reputation of the addresses we hand to customers.
--
-- An address that was used for abuse keeps its reputation after the abuse stops,
-- and the panel had no way to know: 103.118.174.33 was reallocated to a paying
-- customer days after it had been used to scan the internet at 90k packets/sec,
-- carrying whatever listings that earned. The customer inherits mail rejections
-- and CAPTCHA challenges with nothing in the panel to explain why.
CREATE TABLE IF NOT EXISTS ip_reputation (
    id          BIGSERIAL PRIMARY KEY,
    address     INET NOT NULL,
    pool_id     UUID REFERENCES ip_pools(id) ON DELETE CASCADE,

    -- listings names the blocklists that answered "listed". Empty means every
    -- list that was successfully queried said no.
    listings    JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- abuse_score and total_reports come from AbuseIPDB when a key is
    -- configured; -1 for score means "not checked", which must never be
    -- displayed as a clean zero.
    abuse_score   INTEGER NOT NULL DEFAULT -1,
    total_reports INTEGER NOT NULL DEFAULT 0,
    last_reported_at TIMESTAMPTZ,

    -- check_error records why a check could not be completed. A blocklist that
    -- refuses a query returns a value that is NOT "unlisted", and treating a
    -- refusal as a clean result is how a provider convinces itself its space is
    -- fine while mail bounces.
    check_error TEXT NOT NULL DEFAULT '',
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT ip_reputation_address_key UNIQUE (address)
);

-- The admin view is "show me what is listed, worst first".
CREATE INDEX IF NOT EXISTS idx_ip_reputation_listed
    ON ip_reputation (abuse_score DESC);

CREATE INDEX IF NOT EXISTS idx_ip_reputation_checked ON ip_reputation (checked_at);
