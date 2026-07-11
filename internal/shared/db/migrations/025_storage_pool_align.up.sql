-- 025_storage_pool_align: reconcile the live storage_pools schema with the model.
--
-- The live storage_pools table was created by an older migration set (its
-- schema_migrations still lists 002_vm_enhancements / 003_backup_system, which
-- no longer exist in this repo). It uses legacy column names — pool_type,
-- total_bytes, used_bytes — and lacks available_space. Because the table already
-- existed, migration 004's `CREATE TABLE IF NOT EXISTS storage_pools (...)` was a
-- no-op, so the intended columns (type, total_space, used_space, available_space)
-- were never created. GORM's CreatePool inserts the model's column names, hence
-- the failure: `column "type" of relation "storage_pools" does not exist`.
--
-- Safe on both legacy and fresh databases: each rename fires only when the legacy
-- column exists and the target does not, and every add uses IF NOT EXISTS. The
-- whole file runs in a single transaction (see cmd/migrate/applyMigration).

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'pool_type')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'type') THEN
        ALTER TABLE storage_pools RENAME COLUMN pool_type TO type;
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'total_bytes')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'total_space') THEN
        ALTER TABLE storage_pools RENAME COLUMN total_bytes TO total_space;
    END IF;

    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'used_bytes')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'storage_pools' AND column_name = 'used_space') THEN
        ALTER TABLE storage_pools RENAME COLUMN used_bytes TO used_space;
    END IF;
END $$;

-- Backfill any columns still missing (covers the legacy table and any table that
-- never had these at all). NOT NULL adds carry defaults so they stay safe even
-- when rows already exist.
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS type            VARCHAR(50)  NOT NULL DEFAULT 'dir';
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS status          VARCHAR(20)  NOT NULL DEFAULT 'offline';
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS total_space     BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS used_space      BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS available_space BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE storage_pools ADD COLUMN IF NOT EXISTS path            VARCHAR(255) NOT NULL DEFAULT '';
