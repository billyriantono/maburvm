-- MaburVM Panel - Initial Database Schema
-- Created: 2026-03-06
-- Tables: 10 (users, nodes, os_templates, vms, networks, firewall_rules, snapshots, backups, audit_logs, sessions)

-- Custom ENUM types
CREATE TYPE user_role AS ENUM ('admin', 'client');
CREATE TYPE node_status AS ENUM ('active', 'maintenance', 'offline');
CREATE TYPE vm_status AS ENUM ('running', 'stopped', 'suspended', 'creating', 'error');
CREATE TYPE snapshot_status AS ENUM ('pending', 'completed', 'failed');
CREATE TYPE backup_status AS ENUM ('pending', 'in_progress', 'completed', 'failed');

-- 1. users table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role user_role NOT NULL DEFAULT 'client',
    two_factor_secret VARCHAR(255),
    ip_whitelist JSONB DEFAULT '[]'::jsonb,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 2. nodes table
CREATE TABLE nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    ip_address INET NOT NULL,
    status node_status NOT NULL DEFAULT 'offline',
    token VARCHAR(255) NOT NULL UNIQUE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 3. os_templates table
CREATE TABLE os_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    version VARCHAR(50) NOT NULL,
    image_path VARCHAR(500) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    checksum VARCHAR(64) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 4. vms table
CREATE TABLE vms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hostname VARCHAR(100) NOT NULL,
    os_template_id UUID NOT NULL REFERENCES os_templates(id) ON DELETE RESTRICT,
    resources JSONB NOT NULL DEFAULT '{}'::jsonb,
    status vm_status NOT NULL DEFAULT 'stopped',
    source_migration BOOLEAN NOT NULL DEFAULT FALSE,
    vnc_port INTEGER CHECK (vnc_port >= 5900 AND vnc_port <= 5999),
    vnc_password VARCHAR(255),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 5. networks table
CREATE TABLE networks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    ip_address INET NOT NULL,
    bandwidth_limit BIGINT DEFAULT 0,
    vlan_id INTEGER CHECK (vlan_id >= 1 AND vlan_id <= 4094),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 6. firewall_rules table
CREATE TABLE firewall_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    protocol VARCHAR(10) NOT NULL CHECK (protocol IN ('tcp', 'udp', 'icmp', 'all')),
    port_range VARCHAR(50),
    action VARCHAR(10) NOT NULL CHECK (action IN ('allow', 'deny')),
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    source_ip CIDR DEFAULT '0.0.0.0/0'::cidr,
    priority INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 1 AND priority <= 1000),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 7. snapshots table
CREATE TABLE snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    disk_path VARCHAR(500) NOT NULL,
    status snapshot_status NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 8. backups table
CREATE TABLE backups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id UUID NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    storage_provider VARCHAR(100) NOT NULL,
    status backup_status NOT NULL DEFAULT 'pending',
    size BIGINT DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 9. audit_logs table
CREATE TABLE audit_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(255) NOT NULL,
    ip_address INET,
    details JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 10. sessions table
CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token VARCHAR(500) NOT NULL UNIQUE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    ip_address INET,
    user_agent VARCHAR(500),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
