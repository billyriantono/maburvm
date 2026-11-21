-- Per-account sign-in lockout and one-time use of authenticator codes.
-- failed_login_count: wrong passwords or codes in a row since the last good sign-in.
-- locked_until: sign-in is refused until this moment; NULL or past = not locked.
-- totp_last_step: the 30-second step of the last accepted authenticator code;
-- a code from that step or an earlier one is refused.
ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_step BIGINT NOT NULL DEFAULT 0;
