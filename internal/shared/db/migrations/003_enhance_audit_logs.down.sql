-- MaburVM Panel - Rollback Enhanced Audit Logs Schema
-- Remove new columns and indexes from audit_logs

-- Drop indexes
DROP INDEX IF EXISTS idx_audit_logs_resource;
DROP INDEX IF EXISTS idx_audit_logs_user_action;
DROP INDEX IF EXISTS idx_audit_logs_created_at;

-- Drop columns (will also drop data)
ALTER TABLE audit_logs
    DROP COLUMN IF EXISTS resource_type,
    DROP COLUMN IF EXISTS resource_id,
    DROP COLUMN IF EXISTS user_agent,
    DROP COLUMN IF EXISTS before_snapshot,
    DROP COLUMN IF EXISTS after_snapshot;
