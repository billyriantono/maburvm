-- Per-key restrictions. role NULL/'' = act as the owner; 'client' lets an
-- admin mint a key that only sees its own resources. A key never outranks its
-- owner (enforced in code). read_only keys may only make safe (GET) requests.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS role VARCHAR(20);
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS read_only BOOLEAN NOT NULL DEFAULT FALSE;
