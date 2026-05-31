-- Forward DNS: authoritative zones and their resource records.
CREATE TABLE IF NOT EXISTS dns_zones (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(253) NOT NULL,
    ttl         INTEGER NOT NULL DEFAULT 3600,
    primary_ns  VARCHAR(253) NOT NULL DEFAULT '',
    admin_email VARCHAR(253) NOT NULL DEFAULT '',
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_dns_zones_name ON dns_zones(name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS dns_records (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id    UUID NOT NULL REFERENCES dns_zones(id) ON DELETE CASCADE,
    name       VARCHAR(253) NOT NULL,
    type       VARCHAR(10) NOT NULL,
    content    TEXT NOT NULL,
    ttl        INTEGER NOT NULL DEFAULT 3600,
    priority   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_dns_records_zone ON dns_records(zone_id) WHERE deleted_at IS NULL;
