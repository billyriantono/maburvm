-- River Queue Migration Rollback
-- Note: This is a documentation file. River migrations should be managed
-- programmatically or via the River CLI tool.

-- To rollback River migrations, use the River CLI:
--   river migrate-down --database-url="postgres://..."
--
-- Or programmatically:
--   migrator.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{})

-- Tables that would be dropped (in order):
-- DROP TABLE IF EXISTS river_job;
-- DROP TABLE IF EXISTS river_leader;
-- DROP TABLE IF EXISTS river_queue;
