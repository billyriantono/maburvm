-- os_templates.description exists in the Go model but never in the live schema:
-- the table was created by an early migration that predates the field, and the
-- later CREATE TABLE IF NOT EXISTS was a no-op on databases that already had it.
--
-- The effect was that EVERY template update failed outright — GORM writes all
-- columns on Save, so renaming a template returned
-- 'column "description" of relation "os_templates" does not exist'. Reads were
-- unaffected, which is why it went unnoticed.
ALTER TABLE os_templates ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
