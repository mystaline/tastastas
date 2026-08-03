CREATE TABLE IF NOT EXISTS projects (
	project_id     TEXT PRIMARY KEY,
	project_name   TEXT NOT NULL DEFAULT '',
	repository_url TEXT NOT NULL DEFAULT '',
	updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
