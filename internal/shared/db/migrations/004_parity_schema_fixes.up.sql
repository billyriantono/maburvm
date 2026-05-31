-- Parity schema fixes for Virtualizor replacement readiness
-- Adds tables/columns that already exist in Go models but were missing from SQL migrations.

DO $$ BEGIN
    CREATE TYPE backup_type AS ENUM ('manual', 'scheduled');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE backup_schedule_status AS ENUM ('active', 'paused', 'disabled');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

-- Users: model has soft delete and encrypted backup codes.
ALTER TABLE users ADD COLUMN IF NOT EXISTS two_factor_backup_codes VARCHAR(1000);
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP WITH TIME ZONE;
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at);

-- Networks: model/repository/server query expects quota and soft delete.
ALTER TABLE networks ADD COLUMN IF NOT EXISTS bandwidth_quota_gb BIGINT DEFAULT 0;
ALTER TABLE networks ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP WITH TIME ZONE;
CREATE INDEX IF NOT EXISTS idx_networks_deleted_at ON networks(deleted_at);
CREATE INDEX IF NOT EXISTS idx_networks_vm_id ON networks(vm_id);

-- Backups: model has richer metadata than the initial migration.
ALTER TABLE backups ADD COLUMN IF NOT EXISTS storage_path VARCHAR(500) NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS backup_type backup_type NOT NULL DEFAULT 'manual';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS compression VARCHAR(20) DEFAULT 'gzip';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS checksum VARCHAR(64);
ALTER TABLE backups ADD COLUMN IF NOT EXISTS error_message TEXT;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS started_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP WITH TIME ZONE;
CREATE INDEX IF NOT EXISTS idx_backups_deleted_at ON backups(deleted_at);
CREATE INDEX IF NOT EXISTS idx_backups_vm_id ON backups(vm_id);

-- Storage pools assigned to nodes.
CREATE TABLE IF NOT EXISTS storage_pools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    type VARCHAR(50) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'offline',
    total_space BIGINT NOT NULL DEFAULT 0,
    used_space BIGINT NOT NULL DEFAULT 0,
    available_space BIGINT NOT NULL DEFAULT 0,
    path VARCHAR(255) NOT NULL,
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT chk_storage_pool_status CHECK (status IN ('online', 'offline', 'degraded'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_storage_pool_name_node ON storage_pools(name, node_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_storage_pools_node_id ON storage_pools(node_id);
CREATE INDEX IF NOT EXISTS idx_storage_pools_deleted_at ON storage_pools(deleted_at);

CREATE TABLE IF NOT EXISTS storage_volumes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    pool_id UUID NOT NULL REFERENCES storage_pools(id) ON DELETE CASCADE,
    vm_id UUID REFERENCES vms(id) ON DELETE SET NULL,
    size BIGINT NOT NULL,
    format VARCHAR(20) NOT NULL DEFAULT 'qcow2',
    path VARCHAR(255) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT chk_storage_volume_format CHECK (format IN ('qcow2', 'raw'))
);
CREATE INDEX IF NOT EXISTS idx_storage_volumes_pool_id ON storage_volumes(pool_id);
CREATE INDEX IF NOT EXISTS idx_storage_volumes_vm_id ON storage_volumes(vm_id);
CREATE INDEX IF NOT EXISTS idx_storage_volumes_deleted_at ON storage_volumes(deleted_at);

-- Port forwards for VM network NAT rules.
CREATE TABLE IF NOT EXISTS port_forwards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    network_id UUID NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    external_port INTEGER NOT NULL CHECK (external_port >= 1 AND external_port <= 65535),
    internal_port INTEGER NOT NULL CHECK (internal_port >= 1 AND internal_port <= 65535),
    protocol VARCHAR(10) NOT NULL DEFAULT 'tcp' CHECK (protocol IN ('tcp', 'udp')),
    source_ip CIDR DEFAULT '0.0.0.0/0'::cidr,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE INDEX IF NOT EXISTS idx_port_forwards_vm_id ON port_forwards(vm_id);
CREATE INDEX IF NOT EXISTS idx_port_forwards_network_id ON port_forwards(network_id);
CREATE INDEX IF NOT EXISTS idx_port_forwards_deleted_at ON port_forwards(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_port_forwards_unique_external ON port_forwards(external_port, protocol, source_ip) WHERE deleted_at IS NULL;

-- Backup schedules.
CREATE TABLE IF NOT EXISTS backup_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    schedule VARCHAR(100) NOT NULL,
    status backup_schedule_status NOT NULL DEFAULT 'active',
    storage_provider VARCHAR(100) NOT NULL,
    compression VARCHAR(20) DEFAULT 'gzip',
    retention_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    next_run_at TIMESTAMP WITH TIME ZONE,
    last_run_at TIMESTAMP WITH TIME ZONE,
    last_backup_id UUID REFERENCES backups(id) ON DELETE SET NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_schedules_vm_id ON backup_schedules(vm_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_backup_schedules_status ON backup_schedules(status);
CREATE INDEX IF NOT EXISTS idx_backup_schedules_next_run_at ON backup_schedules(next_run_at);
CREATE INDEX IF NOT EXISTS idx_backup_schedules_deleted_at ON backup_schedules(deleted_at);

-- Bandwidth usage table name follows models.BandwidthUsage.TableName(): bandwidth_usages.
CREATE TABLE IF NOT EXISTS bandwidth_usages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    period_start DATE NOT NULL,
    period_end DATE NOT NULL,
    rx_bytes BIGINT NOT NULL DEFAULT 0,
    tx_bytes BIGINT NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    quota_bytes BIGINT NOT NULL DEFAULT 0,
    exceeded BOOLEAN NOT NULL DEFAULT FALSE,
    blocked_at TIMESTAMP WITH TIME ZONE,
    last_reported_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_bw_vm_period ON bandwidth_usages(vm_id, period_start) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_node_id ON bandwidth_usages(node_id);
CREATE INDEX IF NOT EXISTS idx_bandwidth_usages_deleted_at ON bandwidth_usages(deleted_at);
