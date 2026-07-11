-- Reverting only drops the additive column. The legacy<->model column renames are
-- intentionally not reversed: renaming type/total_space/used_space back would
-- re-break CreatePool on a fresh database that legitimately created them via 004.
ALTER TABLE storage_pools DROP COLUMN IF EXISTS available_space;
