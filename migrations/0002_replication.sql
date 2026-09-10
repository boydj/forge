-- Schema version 2: replication bookkeeping (see docs/replication.md).

-- Events applied from another node keep that node's id in origin_id so that
-- replication is idempotent and per-origin cursors can be verified. Events
-- authored locally have origin_id NULL. NULLs are distinct in SQLite unique
-- indexes, so the index only constrains replicated rows.
ALTER TABLE events ADD COLUMN origin_id INTEGER;
CREATE UNIQUE INDEX events_origin ON events(node, origin_id);

-- One cursor per origin node: the highest origin event id applied locally.
CREATE TABLE repl_cursors (
    node           TEXT    PRIMARY KEY,
    last_origin_id INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT    NOT NULL
);

-- The leader-side timestamps that the last successful sync observed, used to
-- skip fetches and metadata pulls when nothing changed.
ALTER TABLE repo_replicas ADD COLUMN leader_updated_at TEXT NOT NULL DEFAULT '';
ALTER TABLE repo_replicas ADD COLUMN leader_pushed_at  TEXT NOT NULL DEFAULT '';
