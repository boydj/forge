# 0009. URL scheme

Date: 2026-09-10
Status: accepted

## Decision

Gemini/Titan paths (host `git.<zone>`):

```
/                                  front page, recent activity
/feed                              forge-wide gemfeed
/~user/                            user profile, repositories
/~user/feed
/~user/repo/                       repository overview (README, clone, refs)
/~user/repo/feed
/~user/repo/tree/<ref>/<path>      source tree / file
/~user/repo/raw/<ref>/<path>       raw blob with detected MIME
/~user/repo/log/<ref>              history
/~user/repo/commit/<id>            revision detail + diff
/~user/repo/refs                   branches and tags
/~user/repo/issues/                issue list
/~user/repo/issues/feed
/~user/repo/issues/<n>             issue with comments
/~user/repo/changes/               change (review) list
/~user/repo/changes/feed
/~user/repo/changes/<n>            change page (ADR 0012)
/~user/repo/changes/<n>/{diff,commits,patch,feed}
/~user/repo/changes/<n>/v<k>/{diff,commits,patch}
/~user/repo/changes/<n>/interdiff/<j>/<k>
/~user/repo/releases/
/~user/repo/releases/feed
/~user/repo/releases/<tag>
/account                           identity, certificates, SSH keys
/account/keys
/status                            node health (public, terse)
```

Titan writes use the same paths with an action suffix, e.g.
`titan://host/~user/repo/issues/new`, `.../issues/<n>/comment`,
`.../changes/<n>/{comment,review,edit,close,reopen,merge}`.

Rule: a write happens only on a Titan upload or on a Gemini INPUT whose
typed value confirms the action. A bare Gemini request never changes state.

SSH: `git@git.<zone>:user/repo.git` (also accepts `~user/repo`, `user/repo`).

Usernames: `^[a-z][a-z0-9-]{0,31}$`. Repository names: `^[a-z0-9][a-z0-9._-]{0,63}$`
excluding names ending in `.git` and the reserved words `feed`, `new`, `edit`.

## Consequences

- Paths are predictable and typeable; `~user` mirrors classic Unix hosting.
- Reserved segments are enforced at creation time.
