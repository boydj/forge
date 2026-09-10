# 0004. Git operations via the git CLI, not a library

Date: 2026-09-10
Status: accepted

## Context

We need to serve `git-upload-pack`/`git-receive-pack`, read trees, blobs,
history and diffs for rendering, run `fsck`, create bundles and fetch for
replication. Candidates: go-git (pure Go), git2go (libgit2, cgo), the git CLI.

## Decision

Use the `git` CLI for everything, via a small adapter package
(`internal/vcs/git`) that implements the VCS interface in `docs/vcs-interface.md`.

- Transport must be `git-upload-pack`/`git-receive-pack` anyway for full
  protocol v2 fidelity.
- Plumbing (`cat-file --batch`, `ls-tree`, `rev-list`, `diff-tree`,
  `for-each-ref`, `bundle`, `fetch`, `fsck`) is stable, fast and well specified.
- go-git has known gaps on large repositories and packfile edge cases; libgit2
  prevents a static binary.
- Every invocation uses an explicit environment (`GIT_CONFIG_NOSYSTEM`,
  `GIT_CONFIG_GLOBAL=/dev/null`, fixed `core.hooksPath`, `-c` safety settings),
  fixed argument lists and timeouts. Repository-controlled configuration is
  never honoured.

## Consequences

- `git` (>= 2.43) is a runtime dependency; packaging pins it.
- Concurrency limits and timeouts on subprocesses are a first-class concern.
- Adding Mercurial later means a second adapter shelling out to `hg`.
