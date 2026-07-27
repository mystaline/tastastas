-- 0001_initial.up.sql — core schema: typed nodes, typed edges, FTS5 lexical index.
-- Vector index (vec0) is created separately in Go (internal/store/sqlite) because
-- its column count depends on the configured embedding dimension.

CREATE TABLE IF NOT EXISTS nodes (
    id            TEXT PRIMARY KEY,
    project_id    TEXT NOT NULL DEFAULT 'default',
    node_type     TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    content_hash  TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'current',
    source_adapter TEXT NOT NULL DEFAULT '',
    source_path   TEXT NOT NULL DEFAULT '',
    importance    REAL NOT NULL DEFAULT 0.5,
    language      TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_nodes_project_type ON nodes(project_id, node_type);
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_language ON nodes(language);

CREATE TABLE IF NOT EXISTS edges (
    from_id     TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    to_id       TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    edge_type   TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 1.0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (from_id, to_id, edge_type)
);

CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);

-- FTS5 lexical index over title+content, external content table pattern.
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
    id UNINDEXED,
    title,
    content,
    content = 'nodes',
    content_rowid = 'rowid'
);

CREATE TRIGGER IF NOT EXISTS nodes_ai AFTER INSERT ON nodes BEGIN
    INSERT INTO nodes_fts(rowid, id, title, content) VALUES (new.rowid, new.id, new.title, new.content);
END;

CREATE TRIGGER IF NOT EXISTS nodes_ad AFTER DELETE ON nodes BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, id, title, content) VALUES ('delete', old.rowid, old.id, old.title, old.content);
END;

CREATE TRIGGER IF NOT EXISTS nodes_au AFTER UPDATE ON nodes BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, id, title, content) VALUES ('delete', old.rowid, old.id, old.title, old.content);
    INSERT INTO nodes_fts(rowid, id, title, content) VALUES (new.rowid, new.id, new.title, new.content);
END;
