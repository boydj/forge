# 0012. Change review model: push to `refs/for/<branch>`, review over Titan, merge with plumbing

Date: 2026-09-10
Status: accepted

## Context

The forge's human interface is Gemini (read; one INPUT line per request) and
Titan (one multi-line text body per request, no forms). Code moves over plain
git via SSH (ADR 0004, docs/git-ssh.md). Every repository has one leader node
that performs all writes (ADR 0011). The `changes`, `reviews` and `comments`
tables of migration 0001 assumed a GitHub-style "source branch -> target
branch" change and need to be finalised.

The workflow must satisfy:

1. No web forms; every write is a git push or a single Titan body.
2. Stock git plus a Titan-capable Gemini client (Lagrange) is enough.
3. Text first: diffs, interdiffs and reviews are readable in gemtext.
4. Reviewers write free-text bodies; inline (per-line) commentary must be
   expressible without a form.
5. Authors iterate by pushing again.
6. Per-file and per-version diffs, plus interdiffs between versions.
7. The forge merges server-side with no working tree.
8. Single writer per repository.
9. Anyone who can read a repository can propose a change to it.
10. Status transitions and feeds.

### Models surveyed

| Model | How a change is proposed | How it is updated | What we take / reject |
| --- | --- | --- | --- |
| GitHub / GitLab pull requests | push a branch to a fork (or the repo), then create the PR in a form; GitLab also `git push -o merge_request.create` | push to the same branch | Rejected as the primary model: needs a fork per contributor (storage, an extra creation step over Titan, cross-repository merge), and a form or push option to open the review. We keep the vocabulary "approve / request changes / comment" and the "new push makes earlier approvals stale" rule. |
| Gerrit | `git push origin HEAD:refs/for/<branch>` on the target repo; no fork, no form | commit message carries a `Change-Id` trailer; a push with a known id becomes a new patch set; `%topic=` groups changes | We take the magic ref, per-version refs and the contributor-not-committer permission split. We reject the mandatory `Change-Id` trailer (needs a commit-msg hook, pollutes upstream history, is the most common Gerrit complaint) and one-change-per-commit. |
| Gitea / Forgejo AGit flow | `git push origin HEAD:refs/for/main -o topic=x`, implemented with git's `proc-receive` hook | same topic updates the open PR; `-o force-push` after a rebase | We take the `proc-receive` implementation technique (the client sees a successful push; `refs/for/*` never exists) and optional `-o topic=`. We reject requiring a topic and the `force-push` option: every push is simply a new version. |
| SourceHut | `git send-email` patch series to a list; "prepare a patchset" web UI; reviewers quote patch lines in email; `v2`, `v3` resubmissions; maintainers `git am` | resend the whole series | We take quoting as the inline-comment mechanism and explicit numbered versions. Email transport is out of scope (no SMTP in the forge); patch *export* is in v1, patch *upload* is deferred (see below). |
| Phabricator / Arcanist | `arc diff` uploads a diff; `Differential Revision:` trailer ties later uploads to the revision | re-run `arc diff` | Rejected: needs a client tool. The idea that "the revision is a series of diffs against a recorded base" is what our versions table stores. |
| Fossil | no pull requests; bundles or patches sent by people the maintainers know | resend | Rejected as a model, but its "review is a conversation, not a form" attitude fits gemtext. |
| Pijul / Jujutsu | a change has an identity that survives rewrites (jj change-id vs commit-id; obslog of versions) | rewriting a commit keeps the change id | We take the concept: a change number identifies the logical change; versions are immutable commits under that number. The id lives in a ref, not in the commit, so history stays clean. |
| `git format-patch` / `am` / `request-pull` / `notes` | text artefacts | resend | `format-patch` output is offered for download (offline review, `git am`). `request-pull` text is what our change page's header amounts to. `notes` were considered for storing reviews in the repository and rejected for v1: keyed by commit id (lost on rebase), not fetched by default, and the database is already the replicated source of truth. |

Key facts verified against git 2.43 (the server version):

- `receive.procReceiveRefs` routes commands whose ref name starts with a
  configured prefix to a `proc-receive` hook that speaks a pkt-line protocol
  and may answer `ok <ref>` with `option refname <other-ref>`,
  `option new-oid`, `option old-oid`, or `ng <ref> <reason>`. The hook
  performs the alternate ref update itself; receive-pack reports the alternate
  name to the client and passes it to `post-receive`. Commands to
  `proc-receive` always arrive with a zero old-oid, so rewritten history is
  accepted without `--force`.
- `pre-receive` runs before `proc-receive`, with objects still in quarantine;
  `proc-receive` runs after quarantine migration, so it may create refs.
- `git merge-tree --write-tree A B` works in a bare repository, prints a tree
  id, exits 0 on a clean merge and 1 with a conflicted-file list on conflicts;
  `git commit-tree` + `git update-ref <ref> <new> <expected-old>` complete an
  atomic, worktree-free merge.
- `git range-diff base1..v1 base2..v2` produces a textual interdiff that
  survives rebases.
- `git apply --cached` with `GIT_INDEX_FILE` pointing at a temporary index
  works in a bare repository; `git am` needs a working tree. `git mailsplit`
  and `git mailinfo` parse mbox patches without one.

## Decision

### 1. One model: fork-less changes pushed to the target repository

A **change** is a numbered review object in a repository, targeting one of
its branches, with one or more immutable **versions**. Versions are commits
pushed by the author to the target repository through a magic ref; the forge
stores them under hidden-from-push but fetchable refs. Review happens in
gemtext comments and reviews uploaded over Titan. Merging is performed by the
forge with git plumbing.

No forks, no `Change-Id`, no client tooling beyond git and a Titan client.
Users who prefer forks may still create their own repositories, but a change
is always proposed by pushing to the repository it targets.

### 2. Push convention

Create a change targeting branch `main`:

```
git push origin HEAD:refs/for/main
```

Push a new version of change 12 (after `commit --amend`, `rebase`, or new
commits):

```
git push origin HEAD:refs/changes/12
```

Optional push options (git >= 2.10 on the client; `-o` may be repeated):

| Option | Meaning |
| --- | --- |
| `-o topic=<name>` | Names the change. A later push to `refs/for/<branch>` with the same topic by the same author updates that open change instead of creating a new one. `[a-z0-9][a-z0-9._-]{0,63}`. |
| `-o change=<n>` | Equivalent to pushing to `refs/changes/<n>`; exists so `refs/for/<branch>` scripts can update. |
| `-o title=<text>` | Sets the title on creation (spaces allowed with `-o`). |

Anything after `refs/for/` is the target branch name, slashes included. Any
other push option, an unparsable option, more than one `refs/for/*` or
`refs/changes/*` command in one push, or a `refs/for/*` command mixed with
`refs/changes/*` is rejected with a message; ordinary `refs/heads/*` and
`refs/tags/*` updates may accompany a change command and are processed by the
normal path.

Matching rules for `refs/for/<branch>`, in order:

1. `-o change=<n>` given: update change `n` (same checks as `refs/changes/n`).
2. `-o topic=<t>` given and an open change by this author with topic `t`
   targeting `<branch>` exists: update it.
3. Otherwise create a new change. There is no author/target heuristic: two
   plain pushes to `refs/for/main` are two changes. The push output always
   prints the exact command for the next version, so nobody has to remember.

Trailers: **not used**. The forge never reads `Change-Id:` or `Change:`
trailers from pushed commits and never asks users to add them. The change
number lives in the ref. The only trailer the forge writes is
`Change: <gemini URL>` in server-generated merge commits.

Storage refs per change `n` and version `k`:

```
refs/changes/<n>/v<k>     immutable; the tip commit of version k
refs/changes/<n>/head     equals the latest version's tip
```

These refs are advertised and fetchable (`git fetch origin
refs/changes/12/head`) so reviewers can test locally; they are never
writable by a direct push (only the `proc-receive` path creates them). The
existing `refs/forge/*` namespace stays hidden. `refs/for/*` never exists as
a ref. Replication (ADR 0011) adds `refs/changes/*` to the fetch refspec.

### 3. Server-side git configuration and hooks

Passed via `git -c` to every `receive-pack` (ADR 0004; threat model T-01 is
amended accordingly):

```
receive.procReceiveRefs=refs/for
receive.procReceiveRefs=refs/changes
receive.advertisePushOptions=true
```

`core.hooksPath` gains `proc-receive` -> `forge hook proc-receive`. The
hook client (`internal/hooks`) speaks pkt-line with receive-pack
(`version=1`, features `push-options`), collects the commands and push
options, sends one JSON `Request{Hook:"proc-receive", Updates, PushOptions}`
to the daemon, and writes the daemon's per-command results back as pkt-lines.
`Response` gains `Results []Result{Ref, OK bool, Reason, RefName, OldOID,
NewOID string}`; `Messages` are still printed to stderr (the client shows
them as `remote: ...`).

`pre-receive` (daemon, `preReceive`) changes:

- Accept `receive-pack` sessions from any account with **read** access
  (SSH `Authorizer` returns the path instead of `ErrForbidden` for readers;
  archived repositories still refuse).
- Allowed ref patterns become `refs/heads/*`, `refs/tags/*`, `refs/notes/*`
  (writers only) plus `refs/for/<branch>` and `refs/changes/<n>` (any reader).
  A reader who pushes anything else is refused with "you can only propose
  changes here: git push origin HEAD:refs/for/<branch>".
- Quota: readers' pushes are limited by `Limits.MaxChangeBytes` (default
  64 MiB per push) and `Limits.MaxOpenChangesPerUser` (default 10 per
  repository); writers follow the repository quota as today.
- All existing object scanning (fsck, dangerous entries, size) applies
  unchanged.

`proc-receive` (daemon, new `procReceive`), inside the repository's write
lock and one database transaction:

1. Parse the command: `refs/for/<branch>` or `refs/changes/<n>`; validate
   options. Reject (`ng`) on: unknown option; `<branch>` does not exist under
   `refs/heads/` ("target branch 'x' does not exist"); `<n>` unknown; change
   merged ("change 12 is merged; push to refs/for/<branch> to start a new
   one"); pusher is neither the change's author nor a writer; deletion
   (zero new-oid); new-oid is not a commit.
2. Compute `base = git merge-base refs/heads/<branch> <tip>`. Reject when
   there is none ("no common history with <branch>"), when
   `git merge-base --is-ancestor <tip> refs/heads/<branch>` ("nothing to
   review: <tip> is already in <branch>"), when `<tip>` equals the current
   head of the change ("already the current version"), or when
   `git rev-list --count base..tip` exceeds `Limits.MaxChangeCommits`
   (default 500).
3. Allocate `number` (per repository, `MAX(number)+1`) for a new change or
   `version = last version + 1` for an existing one. A push to a *closed*
   change by its author reopens it.
4. Update refs atomically with `git update-ref --stdin` (`start`,
   `update refs/changes/<n>/v<k> <tip> 0000...`, `update refs/changes/<n>/head
   <tip> <prev-head-or-zero>`, `commit`).
5. Insert the `change_versions` row (`number`, `head_rev`, `base_rev`,
   `commits`, `pushed_by`); insert or update the `changes` row (`head_rev`,
   `base_rev`, `version`, `state` recomputed = `open`, `updated_at`; on
   creation `title` from `-o title=` else the subject of the oldest commit
   in `base..tip`, `body` from that commit's message body, `topic`).
6. Write the event (`change.open` or `change.update`, path
   `/~o/r/changes/<n>`, payload `{number, version, head, base, commits}`).
   If any step fails, the ref transaction is rolled back (delete the refs
   just created) and the command is answered `ng`.
7. Answer `ok refs/for/<branch>` (or `ok refs/changes/<n>`) with
   `option refname refs/changes/<n>/v<k>`, `option old-oid 0000...`,
   `option new-oid <tip>`, and messages:

```
forge: created change 12 (v1, 3 commits) targeting main
forge:   gemini://git.example/~alice/proj/changes/12
forge:   next version: git push origin HEAD:refs/changes/12
```

`post-receive` ignores `refs/changes/*` updates apart from the existing
size/`RecordPush` bookkeeping; events for changes are written in step 6 so
the row and the ref are consistent.

### 4. Data model (migration `0002_changes.sql`)

`changes` is rebuilt (SQLite cannot alter a CHECK constraint):

```sql
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
                  CHECK (state IN ('open','changes-requested','approved','merged','closed')),
    version       INTEGER NOT NULL DEFAULT 0,   -- latest version number
    head_rev      TEXT    NOT NULL DEFAULT '',  -- tip of latest version
    base_rev      TEXT    NOT NULL DEFAULT '',  -- merge-base recorded for latest version
    merged_rev    TEXT    NOT NULL DEFAULT '',  -- commit that landed on target_branch
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
    number     INTEGER NOT NULL,                -- v1, v2, ...
    head_rev   TEXT    NOT NULL,
    base_rev   TEXT    NOT NULL,                -- merge-base with target at push time
    commits    INTEGER NOT NULL,
    pushed_by  INTEGER NOT NULL REFERENCES users(id),
    created_at TEXT    NOT NULL,
    UNIQUE(change_id, number)
);

ALTER TABLE comments ADD COLUMN version INTEGER NOT NULL DEFAULT 0; -- change version commented on (0 = n/a)
ALTER TABLE reviews  ADD COLUMN version INTEGER NOT NULL DEFAULT 0; -- version reviewed
ALTER TABLE reviews  ADD COLUMN counts  INTEGER NOT NULL DEFAULT 0; -- reviewer had write access
```

`reviews.head_rev` stays (the exact tip reviewed). `source_branch` is gone:
there is no source branch. Comment and review bodies keep the raw uploaded
text; anchors (section 7) are parsed at render time, not stored.

### 5. State machine

```
            push refs/for/<branch>
                    |
                    v
   +------------- open <---------------------------+
   |               |  ^                            |
   |     review    |  | new version (any state     |  push by author
   |               |  | below the dashed line      |  (reopen)
   |               v  | resets to open)            |
   |   changes-requested   approved                |
   |          |               |                    |
   |          +------+--------+                    |
   |                 |                             |
   | close (Titan)   | merge (Titan, writer)       |
   v                 v                             |
 closed            merged                       closed
```

- `open`: no counted verdict on the current version.
- `changes-requested`: at least one counted reviewer's latest review on the
  current version is `request-changes`.
- `approved`: at least one counted `approve` and no counted
  `request-changes` on the current version.
- A new version recomputes from scratch, so it always yields `open`; earlier
  reviews remain visible under their version, marked stale.
- `merged` and `closed` are terminal for reviews and merges; a push by the
  author to a `closed` change reopens it as `open` with a new version; a
  push to a `merged` change is refused.
- Only one review per (reviewer, version) is current: a later review by the
  same reviewer on the same version supersedes the earlier verdict.
- "Counted" means the reviewer had write or admin access when the review
  was recorded (`reviews.counts`). Readers may post reviews only with
  verdict `comment` (section 8).

The `state` column is denormalised for listing and recomputed on every
review, version, close, reopen and merge.

### 6. Gemini pages

All pages follow `internal/web/repo.go` conventions (title, nav links,
`Pre` blocks with an alt text of `diff`, `range-diff` or `patch`, footer).
Every write link is a fixed-prefix action link in the footer section
(T-16) and is shown only to users permitted to perform it.

`/~o/r/changes/` (list; query `?open` default, `?merged`, `?closed`, `?all`):

```
# Changes of alice/proj

=> /~alice/proj/ repository overview
=> /~alice/proj/changes/?open open (3)
=> /~alice/proj/changes/?merged merged
=> /~alice/proj/changes/?closed closed
=> /~alice/proj/changes/feed feed

=> /~alice/proj/changes/12 2026-09-10 #12 Add Titan upload limits (bob, v3, approved, -> main)
=> /~alice/proj/changes/11 2026-09-09 #11 Fix feed dates (carol, v1, changes requested, -> main)

## Propose a change
``` propose
git push origin HEAD:refs/for/main
```
```

`/~o/r/changes/<n>` (change page), sections in order:

1. Header: `# #12 Add Titan upload limits`, then text lines
   `bob -> main, v3, approved. Opened 2026-09-10, updated 2026-09-10.`,
   topic if any, and the body (rendered through the user-content escaper).
2. Links: `diff of v3` (`.../changes/12/diff`), `commits`
   (`.../changes/12/commits`), `patch (mbox)` (`.../changes/12/patch`),
   `fetch:` pre block `git fetch origin refs/changes/12/head && git checkout FETCH_HEAD`,
   `feed` (`.../changes/12/feed`).
3. `## Versions`: one link per version, newest first:
   `=> .../changes/12/v3/diff 2026-09-10 v3 a1b2c3d 3 commits (base 9f8e7d6) current`
   followed, for k >= 2, by `=> .../changes/12/interdiff/2/3 v2 -> v3 interdiff`.
   If the target branch has moved since the current base:
   `main has moved on by 4 commits since v3 was pushed; the merge preview
   below uses the current main.`
4. `## Files (v3)`: per-file link lines
   `=> .../changes/12/v3/diff/internal/web/titan.go internal/web/titan.go +40 -3`.
5. `## Reviews`: newest first, grouped by version, each as
   `### bob approved v3 - 2026-09-10` (or `requested changes`, `commented`;
   suffix `(stale)` for versions below current; `(does not count)` for
   readers) followed by the rendered body with anchors turned into links
   (section 7).
6. `## Comments`: chronological, `### carol - 2026-09-10 on v2` then body.
7. `## Merge` (visible to writers while the state is not terminal): the
   preview result computed on request with `git merge-tree` against the
   current target: either `Fast-forward: main will advance to a1b2c3d.`,
   `Merge commit will be created (main has 4 new commits).`, or
   `Conflicts in: path1, path2 - the author must rebase onto main and push
   a new version.` Followed by the pre block `merge message` showing the
   default message.
8. Footer actions (Titan links, fixed prefix `▸`):
   `comment`, `review`, `edit` (author/writer), `close` (author/writer),
   `reopen` (author/writer, closed only), `merge` (writer, no conflicts).

Sub-pages:

| Path | Content |
| --- | --- |
| `/changes/<n>/diff` | `git diff --stat -p <base> <head>` of the current version, per-file stats then one `diff` pre block; truncated at `MaxDiffBytes` with per-file links. |
| `/changes/<n>/diff/<path>` | one file of the same diff (`-- <path>`). |
| `/changes/<n>/v<k>/diff`, `/changes/<n>/v<k>/diff/<path>` | same for version k using that version's stored base. |
| `/changes/<n>/commits` | `git log --reverse base..head` of the current version, links to `/commit/<id>`. |
| `/changes/<n>/v<k>/commits` | same for version k. |
| `/changes/<n>/interdiff/<j>/<k>` | `git range-diff base_j..v_j base_k..v_k` in a `range-diff` pre block. |
| `/changes/<n>/patch`, `/changes/<n>/v<k>/patch` | `git format-patch --stdout base..head` served as `text/x-patch; charset=utf-8` (an mbox; `git am` on the client). |
| `/changes/<n>/feed` | gemfeed of events for this change (`change.*`, `review`, `comment` with `target_id = n`). |
| `/changes/feed` | gemfeed of all change events of the repository. |

Diffs are rendered against the version's *stored* base so that comments
stay aligned with what the reviewer saw; the merge preview and the merge use
the live target.

### 7. Inline comments: the anchor convention

Bodies uploaded to `comment` and `review` are gemtext (or `text/plain`
treated as gemtext) with one added convention. A line of the form

```
@ <path>[:<line>][ v<k>]
```

starts an **anchored section** that runs until the next `@ ` line or the
end of the body. `<path>` is the file's path on the new side of the diff (or
the old side for deleted files), `<line>` is a line number on the new side,
and `v<k>` names the version (default: the version current when the body was
posted; stored in `comments.version` / `reviews.version`). Quoted material
uses gemtext quote lines (`> `), which is what reviewers naturally produce
by prefixing pasted diff lines. Example review body:

```
approve

Looks good overall, two nits.

@ internal/web/titan.go:57
> 	if size > limit {
> 		return fmt.Errorf("too big")
Include the limit in the message so the client can show it.

@ docs/review-workflow.md
Typo in the second paragraph: "recieve".
```

Rendering: the `@` line becomes a link
`=> /~o/r/changes/12/v3/diff/internal/web/titan.go internal/web/titan.go:57 (v3)`
(plus a second link to `/tree/<head>/<path>` when the file exists at the
head), quote lines are emitted as gemtext quotes, everything else goes
through the normal user-content escaper. Paths that do not appear in the
diff of that version still render, just without the diff link. Nothing about
anchors is stored beyond the raw body; the convention is parsed on display
and documented on the change page's `review` receipt so users learn it where
they need it.

### 8. Titan endpoints

All under `titan://host/~o/r/changes/`. Every endpoint requires a
registered client certificate (60 otherwise), `mime` in `text/plain`,
`text/gemini` or `text/markdown` (treated as gemtext; `text/x-patch` only
where stated), body <= `Limits.MaxTextBytes` (merge: also <= 64 KiB),
CRLF normalised, UTF-8 enforced. Success answers `30 gemini://host/<change
page>`; expected failures answer `20 text/gemini` receipts explaining what to
fix (merge conflicts) or a 5x with a one-line reason. Token policy follows
the threat model (T-19/T-21): `merge`, `close` and `reopen` are destructive
and require a single-use token embedded in the action link the change page
shows; `comment`, `review` and `edit` follow the same policy as issue
comments. Requests at a non-leader node are forwarded (ADR 0011).

| Endpoint | Body | Who | Effect |
| --- | --- | --- | --- |
| `<n>/comment` | comment text (anchors allowed) | any reader | insert comment (`target_kind='change'`, `version` = current), event `comment`. |
| `<n>/review` | first non-empty line is the verdict: `approve`, `request-changes` or `comment`; the rest is the body (anchors allowed) | any reader; `approve`/`request-changes` need write | insert review (`version`, `head_rev`, `counts`), recompute state, event `review`. Author cannot approve their own change (verdict is recorded as `comment`). |
| `<n>/edit` | first line title, blank line, body (`;edit` returns the current text) | author or writer | update title/body, event `change.edit`. |
| `<n>/close` | optional reason (posted as a comment); `size=0` allowed | author or writer | state `closed`, `closed_by`, `closed_at`, event `change.close`. |
| `<n>/reopen` | optional comment; `size=0` allowed | author or writer; target branch must exist | state recomputed, event `change.reopen`. |
| `<n>/merge` | merge commit message; empty body = default message | writer; state not terminal | section 9; event `change.merge`. |

There is no `changes/new` Titan endpoint in v1: the push creates the change.
`changes/new` is reserved for patch upload (section 11).

### 9. Merge algorithm (plumbing only, no working tree)

Run on the leader inside the repository write lock:

```
T   = refs/heads/<target_branch>            # must exist, else 20 receipt "target branch is gone; close or push to another branch"
H   = refs/changes/<n>/head                 # must equal changes.head_rev, else 40 "change is being updated; retry"
old = git rev-parse T
if git merge-base --is-ancestor H T:        20 receipt "already merged" (and mark merged with merged_rev = H)
if git merge-base --is-ancestor T H:        # fast-forward
    new = H
else:
    tree = git merge-tree --write-tree --name-only T H
    exit 1 -> 20 receipt listing the conflicted paths and:
              "Rebase onto <target> and push a new version:
               git fetch origin <target> && git rebase origin/<target> && git push origin HEAD:refs/changes/<n>"
    exit >1 -> 42 with the git error
    new = git commit-tree <tree> -p old -p H -F <message>
          GIT_AUTHOR_NAME/EMAIL = merging user (name, <user>@<hostname>)
          GIT_COMMITTER_NAME/EMAIL = "forge" <forge@<hostname>>
git update-ref -m "forge: merge change <n>" T new old     # compare-and-swap; failure -> 40 "branch moved, try again"
```

Default merge message:

```
Merge change #<n>: <title>

<body>

Change: gemini://host/~o/r/changes/<n>
```

An uploaded body replaces the first two parts; the `Change:` trailer is
always appended. After the ref update, in the same transaction:
`state='merged'`, `merged_rev=new`, `merged_by`, `merged_at`; event
`change.merge` (path `/commit/<new>`); a `push` event is *not* emitted (the
merge event stands in for it); `RecordPush` and `OnPush` run so replicas
fetch. Fast-forward is used whenever possible; a per-repository setting to
always create a merge commit (or to require fast-forward) is a later
addition. Squash and rebase merges are not offered: the author controls the
commits by pushing versions.

### 10. Permissions

Roles: anonymous, reader (registered user on a public repository, or `read`
collaborator), writer (`write`), admin (`admin`, includes the owner). "Author"
is the change's author.

| Action | anonymous | reader | author | writer | admin |
| --- | --- | --- | --- | --- | --- |
| View changes, diffs, patches, feeds | public repos | yes | yes | yes | yes |
| Fetch `refs/changes/*` | public repos | yes | yes | yes | yes |
| Push `refs/for/<branch>` | no | yes | yes | yes | yes |
| Push new version to change n | no | no | yes | yes | yes |
| Comment | no | yes | yes | yes | yes |
| Review: `comment` | no | yes | yes | yes | yes |
| Review: `approve` / `request-changes` | no | no | no (own change) | yes | yes |
| Edit title/body | no | no | yes | yes | yes |
| Close / reopen | no | no | yes | yes | yes |
| Merge | no | no | no | yes | yes |
| Delete a comment (soft) | no | no | own | own | yes |

Private repositories: "reader" requires an explicit collaborator row.
Archived repositories: no pushes, no Titan writes except `close`.
Abuse controls: `MaxOpenChangesPerUser` (10), `MaxChangeBytes` (64 MiB per
push), `MaxChangeCommits` (500), comment/review rate limits identical to
issues, repository owners can block users (existing issue setting applies
to changes too).

### 11. Degradation for email and patch users

- **Export (v1):** `/changes/<n>/patch` gives an mbox that `git am` applies.
  Reviewers without a Titan client can review offline; authors who cannot
  push can hand the mbox to someone who can.
- **Upload (deferred to v2):** `titan://.../changes/new` with
  `mime=text/x-patch` and an mbox body. Implementable without a working
  tree: `git mailsplit` -> per message `git mailinfo` (author, date,
  subject, body, patch) -> temporary index (`GIT_INDEX_FILE`,
  `git read-tree <parent>`) -> `git apply --cached [--3way]` ->
  `git write-tree` -> `git commit-tree`; the target branch comes from a
  first-line header `Target: <branch>` in the body or defaults to the
  default branch. Deferred because stock git over SSH already covers every
  user in scope, the feature needs its own limits (patch size, applied file
  count), and `mailinfo` edge cases (encodings, `[PATCH v2 1/3]` ordering)
  need their own tests. Nothing in the data model changes when it lands: a
  patch upload is just another way to create a version.

### 12. Events and feeds

Event kinds (existing plus new): `change.open`, `change.update` (new
version), `change.edit`, `change.close`, `change.reopen`, `change.merge`,
`review`, `comment` (`payload.target_kind='change'`). Subjects follow the
push-event style, e.g. `bob pushed v3 of change #12 in alice/proj: Add
Titan upload limits`, `carol requested changes on #12 in alice/proj`.

Feeds: `/~o/r/changes/feed` (all change events of the repository),
`/~o/r/changes/<n>/feed` (one change; this is how a reviewer or author
subscribes), and the existing repository, user and global feeds include
change events. Atom equivalents at `.../atom.xml` as for other feeds.

### 13. Required additions to `vcs.Repository`

`MergeBase(a, b)`, `IsAncestor(a, b)`, `CountCommits(base, head)`,
`ListCommits(base, head)` (reverse order), `RangeDiff(base1, head1, base2,
head2, maxBytes)`, `FormatPatch(base, head, w)`, `MergeTree(a, b) (tree,
conflicts []string, err)`, `CommitTree(tree, parents, author, committer,
message)`, `UpdateRef(ref, new, old)`, `UpdateRefs(batch)` (the `--stdin`
transaction), and `DiffRange` gaining an optional path filter. All are thin
wrappers over the listed plumbing commands run through the existing hardened
runner.

## Consequences

- Contributors need nothing but git and a Titan client; a change is one
  push, a review is one upload, a merge is one upload.
- No forks are needed, so objects live where they are merged and storage is
  bounded per change rather than per fork. Fork-based workflows still work
  because pushing `refs/for/*` from a clone of any repository is allowed.
- Upstream history contains no tracking trailers; only server-made merge
  commits carry a `Change:` URL.
- Readers can now open SSH `receive-pack` sessions; the pre-receive rules and
  quotas in section 3 are what keeps that safe. Threat model T-01 must be
  updated to allow `refs/for/*` and `refs/changes/<n>` and to set
  `receive.advertisePushOptions=true` with strict option validation.
- `refs/changes/*` are advertised, so `git ls-remote` output grows with the
  number of versions; the ref count is bounded by the change limits and old
  refs of closed changes are pruned after 90 days (merged changes keep
  theirs).
- ADR 0009's path list gains `changes/feed`, `changes/<n>/{diff,commits,
  patch,interdiff,feed,v<k>/...}` and the Titan suffixes `comment`, `review`,
  `edit`, `close`, `reopen`, `merge`.
- Inline commentary depends on a text convention rather than UI; it is
  robust to rebases (anchors carry a version) but line numbers are not
  re-mapped between versions.

## Open questions

1. Verify on git 2.43 that a `proc-receive` command named `refs/changes/12`
   is handed to the hook even though `refs/changes/12/head` exists (the
   directory/file check lives in the ref transaction, which proc-receive
   commands bypass). If it is not, the update spelling becomes
   `git push origin HEAD:refs/for/<branch> -o change=12` and the push output
   must print that instead.
2. Atomic pushes: the hook should acknowledge `atomic` in its version line
   only if the daemon rolls the whole group back on any `ng`; v1 answers
   without `atomic` and lets git refuse `--atomic` pushes that include a
   change command.
3. Should a repository setting require at least one approval before `merge`
   is offered? Deferred; the page shows outstanding requests and the writer
   decides.
4. Line anchors point at new-side line numbers of a specific version; a
   later version may shift them. Re-mapping via the interdiff is possible
   but not planned.
5. Retargeting a change to another branch is not supported; close and push
   to `refs/for/<other>`.
6. Whether readers' pushes should also be fsck-scanned with stricter limits
   (e.g. refuse blobs over 8 MiB) before the general limit applies.
7. Notifications beyond gemfeeds (Misfin) remain out of scope (research
   note 10).
