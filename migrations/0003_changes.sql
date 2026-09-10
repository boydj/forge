-- Schema version 3: change review model (ADR 0012). The changes table is
-- rebuilt because SQLite cannot alter CHECK constraints; no rows exist yet.

DROP TABLE IF EXISTS reviews;
DROP TABLE IF EXISTS changes;

CREATE TABLE changes (
    id            INTEGER PRIMARY KEY,
    repo_id       INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number        INTEGER NOT NULL,
    author_id     INTEGER NOT NULL REFERENCES users(id),
    title         TEXT    NOT NULL,
    body          TEXT    NOT NULL DEFAULT '',
    topic         TEXT    NOT NULL DEFAULT '',
    target_branch TEXT    NOT NULL,
    state         TEXT    NOT NULL DEFAULT 'open'
                  CHECK (state IN ('open', 'changes-requested', 'approved', 'merged', 'closed')),
    version       INTEGER NOT NULL DEFAULT 0,
    head_rev      TEXT    NOT NULL DEFAULT '',
    base_rev      TEXT    NOT NULL DEFAULT '',
    merged_rev    TEXT    NOT NULL DEFAULT '',
    merged_by     INTEGER REFERENCES users(id),
    closed_by     INTEGER REFERENCES users(id),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    merged_at     TEXT,
    closed_at     TEXT,
    UNIQUE(repo_id, number)
);
CREATE INDEX changes_repo_state ON changes(repo_id, state, updated_at);
CREATE INDEX changes_author_topic ON changes(repo_id, author_id, target_branch, topic);

CREATE TABLE change_versions (
    id         INTEGER PRIMARY KEY,
    change_id  INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,
    head_rev   TEXT    NOT NULL,
    base_rev   TEXT    NOT NULL,
    commits    INTEGER NOT NULL,
    pushed_by  INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT    NOT NULL,
    UNIQUE(change_id, number)
);

CREATE TABLE reviews (
    id          INTEGER PRIMARY KEY,
    change_id   INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    reviewer_id INTEGER NOT NULL REFERENCES users(id),
    verdict     TEXT    NOT NULL CHECK (verdict IN ('approve', 'request-changes', 'comment')),
    body        TEXT    NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    head_rev    TEXT    NOT NULL DEFAULT '',
    counts      INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL
);
CREATE INDEX reviews_change ON reviews(change_id, created_at);

ALTER TABLE comments ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
