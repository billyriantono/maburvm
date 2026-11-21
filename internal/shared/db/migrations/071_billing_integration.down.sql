DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS billing_user_links;
ALTER TABLE os_templates DROP COLUMN IF EXISTS plan_ids;
ALTER TABLE os_templates DROP COLUMN IF EXISTS licensed;
