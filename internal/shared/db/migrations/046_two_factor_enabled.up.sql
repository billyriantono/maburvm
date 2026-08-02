-- Explicit 2FA-enabled flag so an abandoned setup (secret written, never
-- verified) does not lock the user out. Existing users with a secret are treated
-- as enabled to preserve current behavior.
ALTER TABLE users ADD COLUMN IF NOT EXISTS two_factor_enabled BOOLEAN NOT NULL DEFAULT false;
UPDATE users SET two_factor_enabled = true WHERE COALESCE(two_factor_secret, '') <> '';
