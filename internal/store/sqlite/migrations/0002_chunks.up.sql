-- 0002_chunks.up.sql — chunk storage for unified embeddings.
-- Chunks are sub-document units (markdown sections, code functions, etc.)
-- that get their own embeddings for semantic search.

CREATE TABLE IF NOT EXISTS chunks (
    id            TEXT PRIMARY KEY,
    parent_node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    chunk_index   INTEGER NOT NULL,
    chunk_type    TEXT NOT NULL,
    heading_path  TEXT NOT NULL DEFAULT '[]',
    content       TEXT NOT NULL,
    language      TEXT NOT NULL DEFAULT 'markdown',
    source_adapter TEXT NOT NULL DEFAULT '',
    prev_chunk_id TEXT NOT NULL DEFAULT '',
    next_chunk_id TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(parent_node_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_chunks_parent ON chunks(parent_node_id);
CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks(chunk_type);
