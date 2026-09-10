# VCS interface

`internal/vcs` defines the version-control abstraction the forge renders
and replicates through. Git (`internal/vcs/git`) is the only implementation;
Mercurial is planned for M11. ADR 0004 explains why the git CLI rather than
a library.

## The interface (`internal/vcs/vcs.go`)

Types: `RevisionID` (string), `Ref{Name, Kind(branch|tag|other), Target,
Object, When, Message}`, `Signature{Name, Email, When}`,
`Revision{ID, Parents, Author, Committer, Subject, Body, Tree}`,
`TreeEntry{Name, Kind(file|executable|dir|symlink|submodule), Mode, ID,
Size}`, `Blob{ID, Size, Reader}` (caller closes), `DiffStat`,
`Diff{Stats, Patch, Truncated}`, `LogOptions{Path, Skip, Limit}`.

Errors: `ErrNotFound`, `ErrEmptyRepo`, `ErrBadRef`, `ErrBadPath`,
`ErrTooLarge`, `ErrTimeout`, `ErrIsDir`, `ErrNotDir`, `ErrUnexpected`.

```go
type Backend interface {
    Name() string
    Init(ctx, path, defaultBranch string) error
    Open(path string) (Repository, error)
    SetDefaultBranch(ctx, path, branch string) error
    Fetch(ctx, path, remoteURL string) error        // mirror for replication
}

type Repository interface {
    Path() string
    Empty(ctx) (bool, error)
    DefaultBranch(ctx) (string, error)
    Refs(ctx) ([]Ref, error)
    Resolve(ctx, ref string) (RevisionID, error)
    Revision(ctx, id RevisionID) (*Revision, error)
    Log(ctx, id RevisionID, opts LogOptions) ([]*Revision, error)
    Tree(ctx, id RevisionID, path string) ([]TreeEntry, error)
    Blob(ctx, id RevisionID, path string) (*Blob, error)
    Diff(ctx, id RevisionID, maxBytes int64) (*Diff, error)          // against first parent
    DiffRange(ctx, base, head RevisionID, maxBytes int64) (*Diff, error)
    Size(ctx) (int64, error)
    Check(ctx) error
}
```

The interface is read-only plus `Init`/`Fetch`/`SetDefaultBranch`. Writes
to content happen only through the native transport (push over SSH); the
forge never creates commits.

M3 change review (in progress on `main`) extends `Repository` with
server-side merge and ref-transaction methods: `MergeBase`, `IsAncestor`,
`CountCommits`, `ListCommits`, `RangeDiff`, `FormatPatch`, `DiffPath`,
`MergeTree` (`git merge-tree --write-tree`, git >= 2.38), `CommitTree`,
`UpdateRefs` (atomic compare-and-swap, `ErrConflict`), `RefsMatching`
(e.g. `refs/changes/12/`) and `ObjectType`, plus a `proc-receive` hook.
A Mercurial adapter must cover those too (`hg merge`/`hg commit` need a
working copy or the `hg debug*` internals, so a scratch checkout under
`tmp/` is the likely shape); the mapping below covers the read side.

## Git implementation

| Method | git plumbing |
| --- | --- |
| `Init` | `git init --bare --initial-branch=<b> -- <path>`, then `config --local` for `core.logAllRefUpdates`, `core.sharedRepository=0640`, reflog expiry; removes `hooks/` and `description` |
| `Open` | checks `<path>/HEAD` exists |
| `SetDefaultBranch` | `symbolic-ref HEAD refs/heads/<b>` |
| `Fetch` | `fetch --quiet --prune --no-tags --no-write-fetch-head -- <url> +refs/heads/*:refs/heads/* +refs/tags/*:refs/tags/*` |
| `Empty` | `for-each-ref --count=1` |
| `DefaultBranch` | `symbolic-ref --short HEAD` |
| `Refs` | `for-each-ref --format=...` (peeled targets, creator dates, tag messages) |
| `Resolve` | `rev-parse --verify --quiet --end-of-options <ref>^{commit}` after ref-name validation |
| `Revision`, `Log` | `log -n N --format=<NUL-separated fields> --end-of-options <id> -- [path]` |
| `Tree` | `cat-file -t` then `ls-tree -z -l --end-of-options <id> -- <path>/` |
| `Blob` | `cat-file --batch` streaming, bounded to the object size |
| `Diff`, `DiffRange` | `diff-tree -r -M --root --no-color --numstat -z` for stats, `diff-tree -r -M --root -p --no-color` for the patch, truncated at `maxBytes` |
| `Size` | `count-objects -v` (`size` + `size-pack` + `size-garbage`, KiB) |
| `Check` | `fsck --no-dangling --no-progress` |

Every call goes through `Backend.runIn`: a concurrency semaphore
(`git.max_concurrent`), `git.timeout`, captured stdout capped at 64 MiB
(`ErrTooLarge`), stderr folded into `*git.Error`, and the hardened
environment from `Backend.Env()` (listed in `docs/operations.md`). Ref
names and tree paths are validated (`checkRefName`, `checkPath`) before they
become arguments, and arguments always end with `--end-of-options`/`--`.

## What in the codebase is git-specific, and how it is isolated

| Place | Git-specific content | Isolation |
| --- | --- | --- |
| `internal/vcs/git` | everything above; `Env()`; `Command()` for transports | behind `vcs.Backend`/`vcs.Repository` |
| `internal/web` | none: pages use only `vcs.Repository` | clean |
| `internal/forge` | `Forge.Git` is typed `*gitvcs.Backend` (not `vcs.Backend`); `RepoPath` appends `.git`; `Open` returns `ErrUnsupported` when `repositories.vcs != "git"` | the `vcs` column already exists; a second backend needs a small backend registry keyed by that column |
| `internal/forge/hooks.go` | pre-receive policy on `refs/heads/`, `refs/tags/`, `refs/notes/`, the zero id, default-branch deletion; post-receive subjects "created branch", "tagged" | hook requests are already VCS-neutral JSON (`hooks.Request{Updates[]{Old,New,Ref}}`) |
| `internal/hooks` | reads git's `old new ref` lines on stdin and `GIT_QUARANTINE_PATH` for the pushed size | the wire protocol to the daemon is not git-specific |
| `cmd/forge/serve.go` | `installHooks` writes `pre-receive`, `update`, `post-receive` scripts into `<data_dir>/hooks` | one function |
| `internal/sshd` | command grammar accepts only `git-upload-pack`/`git-receive-pack`; argv `git upload-pack --strict --timeout=N -- <path>` / `git receive-pack -- <path>`; `GIT_PROTOCOL` env passthrough; `FORGE_*` variables | `Authorizer` returns a disk path; the exec handler is the only place that knows the verb->argv mapping |
| `internal/store` | `repositories.vcs TEXT DEFAULT 'git'` | ready |

## Mercurial plan (M11)

Goal: `internal/vcs/hg` implementing the same interface by shelling out to
`hg`, plus an SSH exec path for `hg serve --stdio`. Nothing in `internal/web`
should change.

### Mapping

| Interface | Mercurial |
| --- | --- |
| `RevisionID` | the 40-hex changeset node (`{node}`), never local revision numbers, which differ between clones |
| `Ref` branches | named branches from `hg branches -T` (`RefBranch`, target = branch head; a branch with several heads lists each) and bookmarks from `hg bookmarks -T` (also `RefBranch`; bookmarks are the closest thing to git branches for a forge workflow) |
| `Ref` tags | `hg tags -T '{tag}\0{node}\0'` minus the synthetic `tip`; local tags are excluded (`.hg/localtags` is not pushed) |
| `DefaultBranch` | Mercurial has no HEAD; use the `@` bookmark if present, else `default`. `SetDefaultBranch` only updates the forge's `repositories.default_branch` and validates the name exists |
| `Resolve` | `hg log -r <revset> -T '{node}' -l 1` where the revset is constructed, never the raw user string: `id("<hex>")` for hex, `bookmark("<name>")`, `branch("<name>")`, `tag("<name>")` with the name quoted for revset syntax and rejected if it contains `"` or control characters (revsets accept operators such as `::`, `-`, `and`; the raw name must not reach the parser) |
| `Revision`, `Log` | `hg log -r <revset> -T '{node}\0{p1node} {p2node}\0{author|person}\0{author|email}\0{date|rfc3339date}\0{desc}\0' -l N` with `-r "reverse(ancestors(<id>))"` for history, `--follow`-free path filtering with `hg log ... -- <path>`; `Skip` via `-r "reverse(ancestors(X))" ... ` plus `--limit` and slicing (or `first(..., n)` / `limit(..., n, offset)` revsets). Author and committer are the same signature (Mercurial has one) |
| `Tree` | Mercurial has no tree objects: `hg files -r <id> -T '{path}\0{flags}\0' -- "path:<dir>"` (or `hg manifest -r <id> -v --debug` for hashes) and derive the one-level listing from the prefix; directories are synthesised; `x` flag -> `EntryExecutable`, `l` -> `EntrySymlink`; subrepositories from `.hgsub` -> `EntrySubmodule`; size needs `hg files -T '{size}'` |
| `Blob` | `hg cat -r <id> -- <path>` streamed |
| `Diff` | `hg diff -c <id> --git --nocolor` (change introduced by the changeset); `DiffRange`: `hg diff -r <base> -r <head> --git`; stats via `hg diff --stat` |
| `Size` | disk usage of `.hg/store` |
| `Check` | `hg verify -q` |
| `Init`, `Fetch` | `hg init <path>` (no working copy is ever updated: serve with `-U`/never `hg update`); replication `hg pull -f <path-or-ssh-url>` plus `hg bookmarks` sync (`hg pull -B`) |

Hardened environment, analogous to `git.Backend.Env()`: `HGPLAIN=1`,
`HGRCPATH=` (empty: no user or system hgrc), `HGENCODING=UTF-8`, and
`--config` on every command line for `ui.interactive=false`,
`ui.paginate=false`, `hooks.*` set explicitly, `extensions.*` disabled,
`trusted.users=`/`trusted.groups=` so `.hg/hgrc` inside a pushed repository
is never trusted (Mercurial ignores untrusted hgrc files but warns; the warn
must be silenced with `ui.report_untrusted=false`), `phases.publish=true`,
`server.bundle1=false`, `experimental.evolution=` off, and timeouts and
output caps exactly as for git.

### SSH layer

`hg` clients run `hg -R <repo> serve --stdio` over SSH. `internal/sshd`
needs a second command grammar:

```
command := "hg" SP "-R" SP path SP "serve" SP "--stdio"
```

with the same path validation as the git verbs (the client sends the
repository as typed in the clone URL, e.g. `alice/proj`). The server
resolves the disk path through the `Authorizer` exactly as for git, then
runs `hg -R <disk path> serve --stdio` with the hardened config above.

Read-only enforcement follows `hg-ssh` (shipped in Mercurial's `contrib/`):
`--config hooks.prechangegroup.forge=<reject>` and
`--config hooks.prepushkey.forge=<reject>` for accounts without write
access. Push policy and events use `--config hooks.pretxnchangegroup.forge=`
(all changesets of the push, before commit: ACL, quota, phase checks;
`HG_NODE`..`HG_NODE_LAST` name the range) and
`hooks.txnclose.forge=`/`hooks.changegroup.forge=` (after: events,
`RecordPush`), all pointing at `forge hook hg-<name>` which will translate
Mercurial's `HG_*` environment into the existing `hooks.Request` JSON
(bookmark moves arrive via `prepushkey`/`pushkey` with `HG_NAMESPACE=bookmarks`,
`HG_KEY`, `HG_OLD`, `HG_NEW`). Hooks are passed on the command line only; the
repository's `.hg/hgrc` is never consulted.

### Forge-side changes

- `Forge.Git` becomes a `map[string]vcs.Backend` (or `Backend(r)`) chosen by
  `repositories.vcs`; `RepoPath` uses `.git` or `.hg`.
- `repo create --vcs hg` in the CLI and a choice on the `/new` flow.
- `docs/git-ssh.md` gains the `hg` grammar; `docs/titan.md` is unchanged.
- Feed subjects: "pushed N changesets", "moved bookmark", "tagged".
