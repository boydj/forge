-- Schema version 1: identities, repositories, collaboration, activity.
-- Times are RFC 3339 UTC text. Booleans are 0/1 integers.

CREATE TABLE users (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    display_name TEXT    NOT NULL DEFAULT '',
    bio          TEXT    NOT NULL DEFAULT '',
    admin        INTEGER NOT NULL DEFAULT 0,
    disabled     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- TLS client certificates. Identity is the SHA-256 of the SubjectPublicKeyInfo
-- so a re-issued certificate with the same key keeps the identity.
CREATE TABLE certificates (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    spki_sha256  TEXT    NOT NULL UNIQUE,
    cert_sha256  TEXT    NOT NULL,
    subject      TEXT    NOT NULL DEFAULT '',
    label        TEXT    NOT NULL DEFAULT '',
    not_before   TEXT,
    not_after    TEXT,
    created_at   TEXT    NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX certificates_user ON certificates(user_id);

CREATE TABLE ssh_keys (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint  TEXT    NOT NULL UNIQUE,   -- SHA256:base64
    key_type     TEXT    NOT NULL,
    public_key   TEXT    NOT NULL,          -- "type base64" without comment
    label        TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX ssh_keys_user ON ssh_keys(user_id);

CREATE TABLE repositories (
    id             INTEGER PRIMARY KEY,
    owner_id       INTEGER NOT NULL REFERENCES users(id),
    name           TEXT    NOT NULL,
    description    TEXT    NOT NULL DEFAULT '',
    private        INTEGER NOT NULL DEFAULT 0,
    archived       INTEGER NOT NULL DEFAULT 0,
    default_branch TEXT    NOT NULL DEFAULT 'main',
    vcs            TEXT    NOT NULL DEFAULT 'git',
    leader_node    TEXT    NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL,
    pushed_at      TEXT,
    deleted_at     TEXT,
    UNIQUE(owner_id, name)
);
CREATE INDEX repositories_owner ON repositories(owner_id);
CREATE INDEX repositories_updated ON repositories(updated_at);

CREATE TABLE collaborators (
    repo_id    INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT    NOT NULL CHECK (role IN ('read', 'write', 'admin')),
    created_at TEXT    NOT NULL,
    PRIMARY KEY (repo_id, user_id)
);

CREATE TABLE issues (
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    title      TEXT    NOT NULL,
    body       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    closed_at  TEXT,
    UNIQUE(repo_id, number)
);

CREATE TABLE changes (
    id            INTEGER PRIMARY KEY,
    repo_id       INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number        INTEGER NOT NULL,
    author_id     INTEGER NOT NULL REFERENCES users(id),
    title         TEXT    NOT NULL,
    body          TEXT    NOT NULL DEFAULT '',
    state         TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'merged', 'closed')),
    source_branch TEXT    NOT NULL,
    target_branch TEXT    NOT NULL,
    base_rev      TEXT    NOT NULL DEFAULT '',
    head_rev      TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    merged_at     TEXT,
    closed_at     TEXT,
    UNIQUE(repo_id, number)
);

CREATE TABLE comments (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    target_kind TEXT    NOT NULL CHECK (target_kind IN ('issue', 'change')),
    target_id   INTEGER NOT NULL,
    author_id   INTEGER NOT NULL REFERENCES users(id),
    body        TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    deleted_at  TEXT
);
CREATE INDEX comments_target ON comments(target_kind, target_id, created_at);

CREATE TABLE reviews (
    id          INTEGER PRIMARY KEY,
    change_id   INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    reviewer_id INTEGER NOT NULL REFERENCES users(id),
    verdict     TEXT    NOT NULL CHECK (verdict IN ('approve', 'request-changes', 'comment')),
    body        TEXT    NOT NULL DEFAULT '',
    head_rev    TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL
);
CREATE INDEX reviews_change ON reviews(change_id, created_at);

CREATE TABLE releases (
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tag        TEXT    NOT NULL,
    title      TEXT    NOT NULL,
    body       TEXT    NOT NULL DEFAULT '',
    author_id  INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    UNIQUE(repo_id, tag)
);

CREATE TABLE release_assets (
    id         INTEGER PRIMARY KEY,
    release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    mime       TEXT    NOT NULL,
    sha256     TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    UNIQUE(release_id, name)
);

-- Append-only activity and replication log. Feeds are views over this table;
-- replicas apply events in id order.
CREATE TABLE events (
    id         INTEGER PRIMARY KEY,
    kind       TEXT    NOT NULL,     -- repo.create, push, issue.open, comment, release, ...
    repo_id    INTEGER REFERENCES repositories(id) ON DELETE SET NULL,
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    subject    TEXT    NOT NULL,     -- one-line, feed entry title
    path       TEXT    NOT NULL,     -- gemini path of the entry
    payload    TEXT    NOT NULL DEFAULT '{}',
    node       TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);
CREATE INDEX events_repo ON events(repo_id, id);
CREATE INDEX events_user ON events(user_id, id);
CREATE INDEX events_kind ON events(kind, id);

-- One-time tokens: certificate enrolment codes and similar.
CREATE TABLE tokens (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL,
    token_hash TEXT    NOT NULL UNIQUE,
    scope      TEXT    NOT NULL DEFAULT '',
    expires_at TEXT    NOT NULL,
    used_at    TEXT,
    created_at TEXT    NOT NULL
);

CREATE TABLE nodes (
    name         TEXT PRIMARY KEY,
    control_addr TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT
);

CREATE TABLE repo_replicas (
    repo_id        INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    node           TEXT    NOT NULL,
    last_event_id  INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT,
    status         TEXT    NOT NULL DEFAULT 'unknown',
    detail         TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, node)
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
