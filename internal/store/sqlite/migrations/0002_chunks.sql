-- 0002_chunks.sql — chunk storage for unified embeddings.
-- Chunks are sub-document units (markdown sections, code functions, etc.)
-- that get their own embeddings for semantic search. Parent nodes remain
-- the graph/impact surface; chunks are the vector search surface.

CREATE TABLE IF NOT EXISTS chunks (
    id            TEXT PRIMARY KEY,          -- e.g. "project/file.go/chunk/0"
    parent_node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    chunk_index   INTEGER NOT NULL,
    chunk_type    TEXT NOT NULL,             -- markdown_section, code_function, code_method, code_struct, conversation_fact
    heading_path  TEXT NOT NULL DEFAULT '[]', -- JSON array: ["Auth","JWT Validation"]
    content       TEXT NOT NULL,
    language      TEXT NOT NULL DEFAULT 'markdown',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(parent_node_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_chunks_parent ON chunks(parent_node_id);
CREATE INDEX IF NOT EXISTS idx_chunks_type ON chunks(chunk_type);