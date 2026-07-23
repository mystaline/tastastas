-- Edge proposals for Tier 3 disambiguation (0.55-0.80 band proposals).
-- Resolves to edge on confirm_links, cleaned on reject.
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

-- Unresolved references for Tier 1 extraction (entity mentions from doc content
-- that haven't matched a known node yet).
CREATE TABLE IF NOT EXISTS unresolved_references (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      TEXT NOT NULL,
    mention         TEXT NOT NULL,
    source_node_id  TEXT NOT NULL,
    relation_type   TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_unresolved_mention ON unresolved_references(project_id, mention);
