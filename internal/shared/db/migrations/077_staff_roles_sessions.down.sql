DROP TABLE IF EXISTS sign_in_events;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS staff_roles;
ALTER TABLE users DROP COLUMN IF EXISTS staff_role;
