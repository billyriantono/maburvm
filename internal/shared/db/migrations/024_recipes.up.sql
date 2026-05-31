-- Per-user first-boot scripts (Virtualizor "Recipes" parity). A recipe's script
-- is injected as cloud-init user-data when a VM is created, running once on
-- first boot. Only the owning user can see or apply their recipes.
CREATE TABLE IF NOT EXISTS recipes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    description VARCHAR(500) NOT NULL DEFAULT '',
    script      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_recipes_user ON recipes(user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_recipes_deleted_at ON recipes(deleted_at);
-- A user cannot have two recipes with the same name.
CREATE UNIQUE INDEX IF NOT EXISTS uq_recipes_user_name ON recipes(user_id, name) WHERE deleted_at IS NULL;
