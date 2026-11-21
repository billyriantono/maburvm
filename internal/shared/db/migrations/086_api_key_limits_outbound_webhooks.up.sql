-- API keys: a scope narrower than the owner's role ('' = full, or read-only
-- when the older read_only flag is set; then 'full', 'read_only', 'vm_power',
-- 'billing') and the addresses a key may be used from (NULL or [] = anywhere).
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS allowed_ips JSONB;

-- Outbound webhooks an operator adds, each with its own URL, signing secret
-- (sealed like every stored credential) and the events it wants.
CREATE TABLE IF NOT EXISTS webhook_endpoints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    url TEXT NOT NULL,
    secret TEXT NOT NULL DEFAULT '',
    events JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A delivery to one of those endpoints. NULL is the billing system's webhook
-- (System settings, API), which every existing row is. Removing an endpoint
-- removes its log.
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS endpoint_id UUID REFERENCES webhook_endpoints (id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, created_at DESC) WHERE endpoint_id IS NOT NULL;
