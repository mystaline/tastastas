DROP TABLE IF EXISTS access_log;
ALTER TABLE nodes DROP COLUMN access_count;
ALTER TABLE nodes DROP COLUMN last_accessed_at;
