# 0003. Metadata in SQLite; content in Git

Date: 2026-09-10
Status: accepted

## Context

Forge state beyond Git objects: users, certificates, SSH keys, repositories,
ACLs, issues, comments, changes, reviews, releases, activity. Options considered:
SQLite + Git, PostgreSQL + Git, Git-backed metadata, hybrids.

## Decision

SQLite (WAL mode) on each node for metadata; bare Git repositories for content.

- Transactions, indexes and search are trivial; backups are one file plus
  Litestream-style streaming or periodic `VACUUM INTO`.
- A node is fully self-contained: no database server to run or fail over.
- Replication of metadata is done at the application layer: the leader for a
  repository owns its mutable metadata and replicas pull an append-only event
  log over the control network (see `docs/replication.md`).
- Git-backed metadata was rejected: it makes queries, ACLs and quotas hard, and
  couples the forge to one VCS.

PostgreSQL is not needed for the intended scale (thousands of repositories,
not millions). Revisit if a global multi-writer metadata store becomes a
requirement.

## Consequences

- `modernc.org/sqlite`, migrations in `migrations/` applied at startup.
- All writes go through the store package; no raw SQL outside it.
- Metadata changes are recorded as events so replicas and feeds share one log.
