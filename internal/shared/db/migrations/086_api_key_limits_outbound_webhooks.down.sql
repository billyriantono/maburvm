-- Without the columns a narrowed or address-limited key would act as a full
-- key from anywhere, so those keys are switched off rather than widened.
UPDATE api_keys SET is_active = FALSE
WHERE scope IN ('vm_power', 'billing') OR (allowed_ips IS NOT NULL AND allowed_ips <> '[]'::jsonb AND allowed_ips <> 'null'::jsonb);
DROP INDEX IF EXISTS idx_webhook_deliveries_endpoint;
DELETE FROM webhook_deliveries WHERE endpoint_id IS NOT NULL;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS endpoint_id;
DROP TABLE IF EXISTS webhook_endpoints;
ALTER TABLE api_keys DROP COLUMN IF EXISTS allowed_ips;
ALTER TABLE api_keys DROP COLUMN IF EXISTS scope;
