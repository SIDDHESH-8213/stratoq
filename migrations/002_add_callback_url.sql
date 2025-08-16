-- 002_add_callback_url.sql
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS callback_url TEXT;
CREATE INDEX IF NOT EXISTS jobs_callback_idx ON jobs(callback_url);
