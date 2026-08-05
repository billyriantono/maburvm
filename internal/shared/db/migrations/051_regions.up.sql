-- Regions: the location a customer chooses when ordering.
--
-- A region is a city, and a node belongs to one. Today each city holds exactly
-- one node, which is why VPCs and floating IPs — both node-scoped — behave
-- exactly like a customer expects region-scoped resources to behave. That stops
-- being true the moment a city gets a second node; see proposals/002-regions.md.
CREATE TABLE IF NOT EXISTS regions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       VARCHAR(64) NOT NULL,
    name       VARCHAR(128) NOT NULL,
    -- ISO 3166-1 alpha-2, used to render the country's flag. Stored rather than
    -- derived from the name so the flag never depends on how a city was spelled.
    country    CHAR(2) NOT NULL DEFAULT 'ID',
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_regions_slug ON regions (slug) WHERE deleted_at IS NULL;

ALTER TABLE nodes
    ADD COLUMN IF NOT EXISTS region_id UUID REFERENCES regions(id) ON DELETE SET NULL,
    -- zone is stored but NOT exposed to customers yet. An availability zone
    -- implies a promise we cannot keep: a VPC spanning zones needs the VXLAN
    -- overlay, a floating IP crossing zones needs shared L2 or /32 routing, and
    -- "spread VMs across zones for resilience" needs failover that does not
    -- exist. The column is added now only so introducing zones later is a
    -- release rather than a migration.
    ADD COLUMN IF NOT EXISTS zone VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_nodes_region ON nodes (region_id);
