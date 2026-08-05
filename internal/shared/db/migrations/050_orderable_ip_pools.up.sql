-- Which public pools a customer may draw a floating IP from.
--
-- Defaults to FALSE so no existing pool becomes self-service by accident: an
-- administrator opts a pool in deliberately. Without this every pool — including
-- ones reserved for infrastructure — would be orderable the moment self-service
-- shipped.
ALTER TABLE ip_pools
    ADD COLUMN IF NOT EXISTS orderable BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_ip_pools_orderable ON ip_pools (orderable) WHERE orderable;
