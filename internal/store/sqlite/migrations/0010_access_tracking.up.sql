CREATE TABLE IF NOT EXISTS access_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT NOT NULL,
    node_id     TEXT NOT NULL,
    session_id  TEXT NOT NULL DEFAULT '',
    accessed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_access_log_node ON access_log(node_id);
CREATE INDEX IF NOT EXISTS idx_access_log_session ON access_log(session_id);
CREATE INDEX IF NOT EXISTS idx_access_log_project ON access_log(project_id);

ALTER TABLE nodes ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_accessed_at TEXT NOT NULL DEFAULT '';
