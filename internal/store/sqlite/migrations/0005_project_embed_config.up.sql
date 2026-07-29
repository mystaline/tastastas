CREATE TABLE IF NOT EXISTS project_embed_config (
    project_id TEXT PRIMARY KEY,
    model_id   TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'dirty',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
