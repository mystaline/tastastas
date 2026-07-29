CREATE TABLE IF NOT EXISTS project_embed_config_new (
    project_id TEXT NOT NULL,
    model_id   TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'dirty',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (project_id, model_id)
);

INSERT OR IGNORE INTO project_embed_config_new (project_id, model_id, status, created_at)
    SELECT project_id, model_id, status, created_at FROM project_embed_config;

DROP TABLE project_embed_config;
ALTER TABLE project_embed_config_new RENAME TO project_embed_config;
