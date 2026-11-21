-- Databases built from the older migration set named this column size_bytes;
-- the model and every query use size.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'storage_volumes' AND column_name = 'size_bytes')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                       WHERE table_schema = current_schema() AND table_name = 'storage_volumes' AND column_name = 'size') THEN
        ALTER TABLE storage_volumes RENAME COLUMN size_bytes TO size;
    END IF;
END $$;
