-- Temporary accounts (support access): the account stops working at expires_at.
ALTER TABLE users ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS support_note TEXT NOT NULL DEFAULT '';
