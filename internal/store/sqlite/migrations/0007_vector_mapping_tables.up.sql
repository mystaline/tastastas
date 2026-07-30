CREATE TABLE IF NOT EXISTS node_vector_model (
    id       TEXT PRIMARY KEY,
    node_id  TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chunk_vector_model (
    id       TEXT PRIMARY KEY,
    chunk_id TEXT NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL
);
