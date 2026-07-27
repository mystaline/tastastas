-- 0003_tier1_tier3.up.sql — edge proposals and unresolved references.

CREATE TABLE IF NOT EXISTS edge_proposals (
    id          TEXT PRIMARY KEY,
    from_id     TEXT NOT NULL,
    to_id       TEXT NOT NULL,
    edge_type   TEXT NOT NULL,
    confidence  REAL NOT NULL,
    reason      TEXT,
    status      TEXT NOT NULL DEFAULT 'pending',
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS unresolved_references (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    mention         TEXT NOT NULL,
    source_node_id  TEXT NOT NULL,
    relation_type   TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_unresolved_mention ON unresolved_references(project_id, mention);
