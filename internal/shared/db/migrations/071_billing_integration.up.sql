-- Template availability: a licensed system (one that costs the operator a
-- licence per VM) and the plans a template is offered on. No plans (NULL or
-- an empty list) = any plan.
ALTER TABLE os_templates ADD COLUMN IF NOT EXISTS licensed BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE os_templates ADD COLUMN IF NOT EXISTS plan_ids JSONB;

-- The billing system's own reference for a customer (e.g. its client number),
-- so a repeat order finds the same account after an email change.
CREATE TABLE IF NOT EXISTS billing_user_links (
    external_ref VARCHAR(255) PRIMARY KEY,
    user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_billing_user_links_user ON billing_user_links (user_id);

-- Events sent to the billing system's webhook: one row per event, kept as the
-- delivery log. The body is stored as sent, so a retry signs the same bytes.
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_created ON webhook_deliveries (created_at DESC);
