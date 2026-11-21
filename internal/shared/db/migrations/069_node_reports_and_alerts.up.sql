-- When a node's agent last reported, and how many checks in a row it has
-- missed. Written by the metrics collector; the Prometheus node gauges and the
-- "mark offline after N missed checks" rule read them.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS last_report_at TIMESTAMPTZ;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS failed_polls INTEGER NOT NULL DEFAULT 0;

-- Start from the newest stored reading, so a node that is already silent when
-- this ships shows how long it has been.
UPDATE nodes n SET last_report_at = m.at
FROM (SELECT node_id::text AS node_id, MAX(recorded_at) AS at
        FROM node_metrics WHERE status <> 'offline' GROUP BY node_id) m
WHERE m.node_id = n.id::text AND n.last_report_at IS NULL;

-- Operator alerts: one row per episode (a node offline until it is back) or
-- per event (a backup that failed). The open row of a key is the alert's
-- state, so it survives a panel restart; the rest is the history.
CREATE TABLE IF NOT EXISTS alerts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    state TEXT NOT NULL,
    title TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    resource_name TEXT NOT NULL DEFAULT '',
    fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    clear_since TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    notified_at TIMESTAMPTZ,
    recovery_notified_at TIMESTAMPTZ,
    delivered BOOLEAN NOT NULL DEFAULT FALSE,
    attempts INTEGER NOT NULL DEFAULT 0,
    delivery TEXT NOT NULL DEFAULT ''
);
-- At most one open alert per key, whatever races to raise it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_alerts_open_key ON alerts (alert_key) WHERE state = 'firing';
CREATE INDEX IF NOT EXISTS idx_alerts_key_fired ON alerts (alert_key, fired_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_fired_at ON alerts (fired_at DESC);
