# Mercurial prototype (M11)

`internal/vcs/hg` implements `vcs.Backend` and `vcs.Repository` by shelling
out to `hg`, the way `internal/vcs/git` shells out to `git` (ADR 0004). Its
purpose is to validate the VCS abstraction against a second system, not to
ship Mercurial hosting. This page records what mapped cleanly, what did not,
and what productising would take.

Tested against Mercurial 7.2.4 installed with `pip install --user mercurial`
(`~/.local/bin/hg`, a Python entry point). Tests skip when `hg` is not on
PATH.

## Hardened environment

Every invocation runs with `HGPLAIN=1` (no aliases, defaults, colour,
localisation), `HGRCPATH=/dev/null` (no user or system hgrc, hence no
extensions), `HGENCODING=UTF-8`, `HGUSER=forge`, and a fixed PATH. Each
command line is prefixed with `--config` guards (`ui.interactive=false`,
`ui.paginate=false`, `ui.report_untrusted=false`, `ui.color=never`,
`phases.publish=true`, `server.bundle1=false`, `experimental.evolution=`,
`extensions.{evolve,topic,largefiles,lfs}=!`). Timeouts, the concurrency
semaphore, the 64 MiB stdout cap and stderr folding (`*hg.Error`) mirror the
git runner.

HOME is *not* redirected to a temp dir (the git backend does that): a
pip-installed `hg` imports `mercurial` from `~/.local/lib`, and HGRCPATH
already hides `~/.hgrc`.

Revsets are built only from validated parts: ids are 40-hex (`id("...")`),
names are checked by `checkName` (no `"`, `\`, `:`, control characters) and
always wrapped as `"literal:<name>"`. Without `literal:`, `branch("<hex>")`
silently interprets the string as a changeset and returns its whole branch.
Paths use `file("path:<p>")` / `-- path:<p>` so no glob is ever expanded.

## What maps cleanly

| Interface | Mercurial |
| --- | --- |
| `RevisionID` | 40-hex changeset node; local revision numbers are never used |
| `Init` | `hg init -- <path>`; writes `.hg/hgrc` (publishing, no bundle1) and `.hg/forge-default-branch` |
| `Open` | `<path>/.hg/requires` exists |
| `Fetch` | `hg pull -q -f -- <url>` (changesets, bookmarks, `.hgtags`) |
| `Empty` | `hg log -l 1 -T {node}` |
| `Refs` | `hg branches -T` (open heads) + `hg bookmarks -T`, both `RefBranch`; `hg tags -T` minus `tip` and local tags, `RefTag`; `When` from `{date\|rfc3339date}` |
| `Resolve` | bookmark, tag, `max(branch(...))`, then `id(<hex prefix>)`, each in `present(...)` |
| `Revision`, `Log` | `hg log -r <revset> -T` with `\0`/`\x01` separators; `Log` = `limit(reverse(ancestors(id)) [and file("path:P")], N, skip)`; committer = author; `Tree` carries the named branch |
| `Tree` | `hg manifest -T {path}{hash}{size}` + `hg files -T {path}{flags}` (the manifest template has no symlink flag); directories synthesised, sorted first, ID "" and Size -1; modes rendered git-style |
| `Blob` | `hg files -r -- path:P` to classify (file / directory / missing), then `hg cat` streamed and bounded to the size |
| `Diff` | `hg diff --git -c <id>`; `DiffRange`/`DiffPath` = `hg diff --git -r a -r b [-- path:P]`. Stats are computed by parsing the git-format patch while streaming; the whole patch is scanned for stats but only `maxBytes` retained |
| `Size` | bytes under `.hg` |
| `Check` | `hg verify -q` |
| `MergeBase` | `ancestor(a, b)` (unrelated: empty -> `ErrNotFound`) |
| `IsAncestor` | `a and ancestors(b)` |
| `CountCommits`, `ListCommits` | `only(head, base)` (exactly git's `base..head`); `last(only(...), n)` yields the newest *n* oldest-first, matching the git adapter |
| `FormatPatch` | `hg export --git -r only(head, base)` streamed, `ErrTooLarge` past `maxBytes` |
| `RefsMatching` | `refs/heads/` (branches + bookmarks) and `refs/tags/` only |
| `ObjectType` | `"commit"` or `ErrNotFound` (manifest/filelog nodes are not addressable) |

Everything above is covered by `hg_test.go` with a real repository fixture
(named branch, bookmark, tag, rename, mode change, symlink, binary file).

## What does not map

Returned as `hg.ErrUnsupported` (to be moved to `vcs.ErrUnsupported`):

- **`RangeDiff`**. No equivalent; `hg obslog --patch` needs the evolve
  extension and obsolescence markers.
- **`MergeTree`**. `hg merge` requires a working copy. Options: (a) a scratch
  checkout per merge under `<data>/tmp` (`hg share`, `hg update`, `hg merge
  --tool internal:merge3`, `hg commit`, then strip or keep as a draft),
  (b) Mercurial's in-memory merge (`mergemod.update(..., wc=overlayworkingctx)`)
  through a small Python helper shipped with the forge, since it is not
  exposed on the command line. Either way the result is a *changeset*, not a
  tree id: the interface's `tree -> CommitTree` split is git-shaped.
- **`CommitTree`**. No tree objects. With (a)/(b) above, merge and commit
  are one step, so `MergeTree` would have to return a changeset id and
  `CommitTree` become a no-op or a `hg commit --amend`-like rewrite of
  author/message. Interface change to consider: `Merge(ours, theirs,
  author, message) (RevisionID, conflicts, error)` with git implementing it
  as merge-tree + commit-tree.
- **`UpdateRefs`**. Named branches are changeset metadata, not refs: a
  branch "moves" only by committing on it and cannot be deleted or
  force-reset (only closed). Bookmarks are refs-like but the command line
  offers no compare-and-swap transaction; `hg bookmark -r X name` is
  last-writer-wins. A CAS could be built with the repository lock
  (`hg debuglock`) around read-then-`bookmark`, or in the Python helper
  with `repo.lock()` + `bookmarks.applychanges`.
- **`DefaultBranch`/`SetDefaultBranch`**: Mercurial has no HEAD. The
  prototype stores the choice in `.hg/forge-default-branch`; the `@`
  bookmark is what `hg clone` checks out, so a production version should
  also move `@`.
- **`SetDefaultBranch` on an empty repository** accepts only `default`.
- **Committer signatures, tree ids, annotated tags**: absent; `Revision.Tree`
  carries the branch name, `Ref.Message` is always empty.
- **`Fetch` never deletes**: a bookmark or branch removed at the source
  persists on the mirror (`hg pull` is additive; `hg strip` is destructive).
- **Subrepositories** (`.hgsub`) are not turned into `EntrySubmodule`.
- **Cost**: each call is a Python process (~150 ms startup); `Tree` and
  `Log` run two, `Blob` two, the plumbing methods 2–3 (an explicit
  existence check is needed because `id("<unknown>")` is an empty set and
  `ancestor(x, empty) = x`). A production adapter would batch through
  `hg serve --cmdserver pipe` (one long-lived process per repository slot).

## Change review: an equivalent to `refs/for/*`

The git model (ADR 0012) receives a push to `refs/for/<target>` in
`proc-receive`, never creates the ref, and stores the revision under
`refs/changes/<n>/<v>`. Mercurial has no client-controlled ref namespace, so
the intent has to be carried differently. Proposed design:

1. **Client side**: `hg push -B change/<target>` (a bookmark whose name
   starts with `change/`) or `hg push --config forge.change=<target>`... the
   former needs no client configuration and travels in the `pushkey`
   part of the bundle, so the bookmark name is the equivalent of the
   `refs/for/<target>` ref.
2. **Server side**: `hg serve --stdio` runs with
   `--config hooks.pretxnchangegroup.forge=forge hook hg-pretxnchangegroup`
   and `--config hooks.prepushkey.forge=forge hook hg-prepushkey`. The
   `prepushkey` hook sees `HG_NAMESPACE=bookmarks HG_KEY=change/<target>
   HG_NEW=<node>` (`HG_OLD` for an update); it translates that to
   `hooks.Request{Updates: [{Ref: "refs/for/<target>", New: node}]}`, the
   daemon opens or updates the change exactly as for git, and the hook
   **rejects** the pushkey (exit 1) so the bookmark is never created on the
   server. The changesets themselves were already accepted by
   `pretxnchangegroup` (as drafts: `phases.publish=false` for change pushes,
   so they do not become public) and stay in the store, which is what
   `refs/changes/<n>/<v>` provides in git: the review revision is reachable
   by node even though no branch points at it.
3. **Bookkeeping**: the forge records `<node>` per change version in the
   `change_versions` table (already VCS-neutral); nothing named
   `refs/changes/` is needed. Draft changesets not referenced by any change
   version are garbage: a periodic `hg strip` (or leaving them; Mercurial
   has no gc) is a productising decision.
4. **Submit**: with `MergeTree` implemented through a scratch checkout the
   merge changeset is committed on the target branch; publishing it
   (`hg phase -p`) is the equivalent of updating `refs/heads/<target>`.
5. **Superseding a version** works as in git: a new push to the same
   bookmark name with a new node.

Transaction safety comes for free: Mercurial runs `pretxnchangegroup` and
`prepushkey` inside the push transaction, so a rejection rolls back the
changesets too if the daemon says no.

## SSH transport

Clients run `hg -R <repo> serve --stdio` over SSH. `internal/sshd` needs a
second command grammar next to `git-upload-pack`/`git-receive-pack`:

```
command := "hg" SP "-R" SP path SP "serve" SP "--stdio"
```

with the same path validation and `Authorizer` lookup as git (the path is
what the user typed in the clone URL). The exec handler then runs

```
hg -R <disk path> <configArgs...> serve --stdio
```

with `Backend.Env()` and, for read-only accounts,
`--config hooks.prechangegroup.forge=/bin/false --config
hooks.prepushkey.forge=/bin/false` (the `hg-ssh` contrib recipe). Push
policy and events use `hooks.pretxnchangegroup.forge`, `hooks.prepushkey.forge`,
`hooks.txnclose.forge` -> `forge hook hg-<name>`, which turn `HG_NODE`,
`HG_NODE_LAST`, `HG_NAMESPACE`/`HG_KEY`/`HG_OLD`/`HG_NEW` into the existing
`hooks.Request` JSON. Hooks are passed on the command line; the repository's
`.hg/hgrc` (written by `Init`, never by clients) carries only phase and
bundle settings. Mercurial's wire protocol has no `GIT_PROTOCOL`
equivalent; `--config server.bundle1=false` pins the modern format.

## Storage layout and metadata

- Repositories: `<data>/repos/<owner>/<name>.hg` (a directory containing
  `.hg`; no working copy is ever updated). `Forge.RepoPath` currently appends
  `.git` unconditionally.
- `repositories.vcs` (`migrations/0001_init.sql`, `TEXT NOT NULL DEFAULT
  'git'`) already exists; `Forge.Open` returns `forge.ErrUnsupported` for
  anything but `git`.
- Backups: `hg bundle -a` replaces `git bundle create --all`; replication
  uses `Backend.Fetch` (`hg pull`), so `docs/replication.md` applies.
- Size accounting reads `.hg` directly (no `count-objects`).

## Required edits to existing files (not made by the prototype)

- `internal/vcs/vcs.go`: add `ErrUnsupported = errors.New("vcs: unsupported")`
  and delete `hg.ErrUnsupported`; document `Revision.Tree` as
  "tree id or backend-specific container" and `Blob.ID` as opaque.
- `internal/vcs/vcs.go`: consider `Merge(ours, theirs, author, message)`
  replacing the `MergeTree` + `CommitTree` pair (git keeps both underneath).
- `internal/forge/forge.go`: `Forge.Git *gitvcs.Backend` -> `Backends
  map[string]vcs.Backend` (or `Backend(vcs string)`); `RepoPath` chooses
  `.git`/`.hg` by `repositories.vcs`; `Open` dispatches on it. `backup.go`
  calls `f.Git.Command(... "bundle" ...)` and `gc --auto` directly and needs
  a per-backend `Bundle`/`Maintain` method.
- `internal/forge/repos.go`, `cmd/forge`: `repo create --vcs hg`, `/new`
  form field.
- `internal/sshd`: the `hg` command grammar above; `docs/git-ssh.md`.
- `internal/hooks`, `cmd/forge/serve.go`: `forge hook hg-*` subcommands
  translating `HG_*` into `hooks.Request`.
- `internal/web`: nothing required; pages use `vcs.Repository`. Rendering
  should tolerate `Ref.Message == ""`, `TreeEntry.ID == ""` for directories
  and `Revision.Tree` not being a hash.
- `docs/vcs-interface.md`: link here; the `Resolve` row should say
  `literal:`.
- Packaging: `hg` (>= 6.x; 7.2 tested) as an optional runtime dependency.

## Productising checklist

- [ ] Move `ErrUnsupported` into `vcs`; adopt a `Capabilities()` method so
      the web and change-review layers can hide unsupported actions instead
      of failing at call time.
- [ ] Command server (`hg serve --cmdserver pipe`) pool to amortise Python
      startup; keep the per-call timeout semantics.
- [ ] `MergeTree`/`CommitTree` via scratch checkout or Python helper;
      decide the interface shape.
- [ ] `UpdateRefs` for bookmarks with CAS under the repository lock.
- [ ] `refs/for` equivalent: `change/<target>` bookmark + `prepushkey`
      rejection, draft-phase pushes, garbage policy for orphaned drafts.
- [ ] SSH grammar, hook subcommands, read-only enforcement, quota
      (`HG_NODE` range size via `hg log -r 'first::last' --stat`?) and
      `RecordPush`.
- [ ] `@` bookmark maintenance for `SetDefaultBranch`; branch closing on
      "delete".
- [ ] Mirror deletions on `Fetch` (compare bookmark lists, `hg bookmark -d`).
- [ ] Subrepositories (`.hgsub`) as `EntrySubmodule`; largefiles/LFS stay
      disabled.
- [ ] Backup (`hg bundle -a`), integrity (`hg verify` cadence), size quotas.
- [ ] Web: badge for the VCS on repository pages, clone URL help text for
      `hg clone ssh://...`.
- [ ] Security review of `--config` surface: every `hg serve --stdio` flag
      is set by the forge; confirm HGPLAIN plus HGRCPATH leaves no path for a
      pushed file to be read as configuration.
