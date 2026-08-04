-- Track the template image download size so the UI can display it.
-- Populated from the image URL's Content-Length on create and on
-- refresh. 0 = unknown (not yet fetched).
ALTER TABLE os_templates ADD COLUMN IF NOT EXISTS size_bytes BIGINT NOT NULL DEFAULT 0;
