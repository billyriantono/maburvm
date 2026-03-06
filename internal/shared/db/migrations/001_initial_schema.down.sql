-- MaburVM Panel - Rollback Initial Database Schema
-- Drop all tables and types in reverse order of creation

-- Drop tables in reverse dependency order
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS audit_logs CASCADE;
DROP TABLE IF EXISTS backups CASCADE;
DROP TABLE IF EXISTS snapshots CASCADE;
DROP TABLE IF EXISTS firewall_rules CASCADE;
DROP TABLE IF EXISTS networks CASCADE;
DROP TABLE IF EXISTS vms CASCADE;
DROP TABLE IF EXISTS os_templates CASCADE;
DROP TABLE IF EXISTS nodes CASCADE;
DROP TABLE IF EXISTS users CASCADE;

-- Drop custom ENUM types
DROP TYPE IF EXISTS backup_status CASCADE;
DROP TYPE IF EXISTS snapshot_status CASCADE;
DROP TYPE IF EXISTS vm_status CASCADE;
DROP TYPE IF EXISTS node_status CASCADE;
DROP TYPE IF EXISTS user_role CASCADE;
