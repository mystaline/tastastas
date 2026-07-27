-- 0004_job_marker.up.sql — interrupted-run detection marker.
-- A single-row table: set on job start, cleared on clean finish.
-- On next startup, if row exists, prior run was interrupted.

CREATE TABLE IF NOT EXISTS job_marker (
    id        INTEGER PRIMARY KEY CHECK (id = 1),
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
