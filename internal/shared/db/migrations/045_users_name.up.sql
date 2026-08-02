-- Display name for users (shown in the panel; collected on the Add User form).
ALTER TABLE users ADD COLUMN IF NOT EXISTS name VARCHAR(255) NOT NULL DEFAULT '';
