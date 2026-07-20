-- 0001_init.sql — core schema: typed nodes, typed edges, FTS5 lexical index.
-- Vector index (vec0) is created separately in Go (internal/store/sqlite) because
-- its column count depends on the configured embedding dimension.

CREATE TABLE IF NOT EXISTS nodes (
    id            TEXT PRIMARY KEY,          -- qualified id, e.g. "acme/backend/billing-service" or "acme/prd/coupon-redeem"
    project_id    TEXT NOT NULL DEFAULT 'default',
    node_type     TEXT NOT NULL,             -- repo, service, boundary, shared-lib, prd, api-spec, erd, test-case,
                                              -- visual-design, architecture-decision, generic-doc, fact, entity
    title         TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    content_hash  TEXT NOT NULL DEFAULT '',  -- sha256 of content, used to detect changes for impact propagation
    status        TEXT NOT NULL DEFAULT 'current', -- current, stale, superseded
    source_adapter TEXT NOT NULL DEFAULT '', -- gitrepo, obsidian, docwalk, conversation, manual
    source_path   TEXT NOT NULL DEFAULT '',
    importance    REAL NOT NULL DEFAULT 0.5, -- 0..1, used in retrieval scoring
    language      TEXT NOT NULL DEFAULT '',  -- programming language for code files (go, python, etc.)
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_nodes_project_type ON nodes(project_id, node_type);
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_language ON nodes(language);

CREATE TABLE IF NOT EXISTS edges (
    from_id     TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    to_id       TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    edge_type   TEXT NOT NULL,   -- depends-on, belongs-to, implements, tests, specifies, derived-from, related, superseded-by
    confidence  REAL NOT NULL DEFAULT 1.0, -- 1.0 = explicit/config-derived, <1.0 = similarity-inferred
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (from_id, to_id, edge_type)
);

CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);

-- FTS5 lexical index over title+content, external content table pattern
-- keeps `nodes` as the source of truth; fts index is kept in sync via triggers.
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
