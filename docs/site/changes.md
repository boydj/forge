# Proposing and reviewing changes

A **change** is a numbered proposal to merge some commits into a branch of
a repository. You propose it with `git push`, discuss it on its page,
update it by pushing again, and a maintainer merges it with one Titan
upload. There are no forks to create and no forms: git plus a client that
speaks Titan is all you need.

You need an account with an SSH key and read access to the repository.
Every public repository accepts changes from any registered user; private
ones from collaborators.

## Propose

Clone the repository itself (not a fork), commit on a local branch, and
push to `refs/for/<branch>`:

```
git clone git@git.as215520.net:alice/proj.git
cd proj
git switch -c fix-feed-dates
# edit, commit
git push origin HEAD:refs/for/main
```

The server answers with the change number and its page:

```
remote: forge: created change 12 (v1, 2 commits) targeting main
remote: forge:   gemini://git.as215520.net/~alice/proj/changes/12
remote: forge:   next version: git push origin HEAD:refs/changes/12
```

The title is the subject of your oldest new commit and the description is
that commit's message body. Push options: `-o title="..."` sets the title;
`-o topic=<name>` names the change, and pushing again to `refs/for/main`
with the same topic updates it instead of opening a new one. Otherwise
every push to `refs/for/<branch>` opens a new change.

Refused, with a message: a target branch that does not exist, commits
already in the target, more than 500 commits, a push over 64 MiB, or more
than 10 open changes of yours in one repository.

## Update: push a new version

Rewrite or extend your commits as you like (`--amend`, `rebase -i`, new
commits), then push to the change number; no `--force` needed:

```
git push origin HEAD:refs/changes/12
```

Each push is a new version (`v2`, `v3`, ...). Earlier versions stay
readable, and the page shows an *interdiff* between versions so reviewers
see only what moved. A new version sets the review state back to *open*;
earlier verdicts are kept but marked stale.

To make plain `git push` do this on that branch:

```
git config branch.fix-feed-dates.remote origin
git config branch.fix-feed-dates.merge refs/changes/12
git config push.default upstream
```

Rebase onto a moved target with `git fetch origin main && git rebase
origin/main`, then push the new version.

Title and description are edited from the change page's *edit* action
(Titan; Lagrange loads the current text). Upload the title on the first
line, a blank line, then the description.

## Read

`/~alice/proj/changes/` lists open changes (links for merged, closed,
all). `/~alice/proj/changes/12` shows the header (title, author, target,
version, state), the versions with their diffs and interdiffs, per-file
diffs, reviews and comments, the merge preview for maintainers, and the
actions you may take.

| Path | Shows |
| --- | --- |
| `.../changes/12/diff` | full diff of the current version |
| `.../changes/12/diff/<path>` | one file |
| `.../changes/12/v2/diff` | diff of version 2 |
| `.../changes/12/interdiff/2/3` | what changed between v2 and v3 |
| `.../changes/12/commits` | the commits, oldest first |
| `.../changes/12/patch` | the series as an mbox for `git am` |
| `.../changes/12/feed` | subscribe to this change |

Test it locally: `git fetch origin refs/changes/12/head && git checkout
FETCH_HEAD` (or `refs/changes/12/v2` for a version).

## Review

Reviews and comments are Titan uploads, one text body each. In Lagrange
open the *review* or *comment* link in the change page's footer, type in
the upload dialog with your identity selected, and send. From a terminal,
for example with gmid's `titan(1)`:

```
titan -C cert.pem -K key.pem -m text/gemini \
  titan://git.as215520.net/~alice/proj/changes/12/review review.gmi
```

**Review** (`.../changes/12/review`): the first non-empty line is the
verdict, `approve` or `request-changes` (maintainers; authors cannot
approve their own change) or `comment` (anyone who can read). The rest is
your review as gemtext.

**Comment** (`.../changes/12/comment`): the whole body, for discussion
that is not a verdict.

**Pointing at a line.** Start a section with an `@` line naming the file,
optionally the line and the version, and quote the lines you mean with
`> `; the forge links the `@` line to that file's diff:

```
approve
Looks good, two nits.
@ internal/web/titan.go:57
> 	if size > limit {
Include the limit in the message so the client can show it.
@ docs/guide.md v2
Typo in the second paragraph.
```

A section runs until the next `@` line or the end. Your latest review on
a version replaces your earlier verdict on it.

## Merge (maintainers)

The change page's **Merge** section says what will happen: fast-forward,
merge commit, or a list of conflicting files. Follow the *merge* link
(Titan) and upload the merge commit message; an empty upload uses the
default shown. The target branch is updated atomically. If it moved while
you were reading, you get "branch moved, try again"; on conflicts nothing
changes and the page tells the author what to rebase. There is no squash
or rebase merge: the author shapes the commits by pushing versions.

## Close and reopen

Author or maintainer: `.../changes/12/close` (Titan; an optional body is
posted as the reason) and `.../changes/12/reopen`. Pushing a new version
to a closed change reopens it. Merged changes are final; continue the work
with a new change.

## Permissions

| Action | Needs |
| --- | --- |
| read, fetch `refs/changes/*`, download patches | read access |
| propose, comment, review with `comment` | a registered user with read access |
| push a new version | the author, or write access |
| `approve`, `request-changes`, merge | write access |
| edit title or description, close, reopen | the author, or write access |

## Messages from the server

| Message | Meaning |
| --- | --- |
| `you can only propose changes here` | you pushed to a branch without write access; push to `refs/for/<branch>` |
| `target branch 'x' does not exist` | check `/~alice/proj/refs` |
| `nothing to review: <sha> is already in main` | already merged, or the wrong ref |
| `already the current version` | the tip you pushed is unchanged |
| `change 12 is merged` | open a new change |
| `not the author of change 12` | only the author or a maintainer pushes versions |
| `Conflicts in: ...` | rebase onto the target and push a new version |
| `branch moved, try again` | reload the change page and merge again |
