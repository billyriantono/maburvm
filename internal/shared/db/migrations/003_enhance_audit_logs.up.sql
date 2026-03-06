-- MaburVM Panel - Enhanced Audit Logs Schema
-- Adds resource tracking, snapshots, and user agent to audit_logs

-- Add new columns to audit_logs table
ALTER TABLE audit_logs
    ADD COLUMN IF NOT EXISTS resource_type VARCHAR(50),
    ADD COLUMN IF NOT EXISTS resource_id UUID,
    ADD COLUMN IF NOT EXISTS user_agent VARCHAR(500),
    ADD COLUMN IF NOT EXISTS before_snapshot JSONB,
    ADD COLUMN IF NOT EXISTS after_snapshot JSONB;

-- Create index for efficient resource-based queries
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user_action ON audit_logs(user_id, action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);
