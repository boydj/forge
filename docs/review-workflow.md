# Proposing, reviewing and merging changes

Decision: ADR 0012. This is the user-facing guide; paths are relative to
`gemini://git.<zone>/` and `titan://git.<zone>/` (same host and port).

A **change** is a numbered proposal to merge some commits into a branch of a
repository. You propose it with `git push`, discuss it on its Gemini page,
update it by pushing again, and a maintainer merges it with one Titan upload.
There are no forks to create, no forms to fill in and no special client
tools: git plus a Gemini client that speaks Titan (Lagrange does) is all you
need.

## What you need

- An account with a registered SSH key (`/account/keys`) and a client
  certificate selected for `gemini://git.<zone>/` in your client.
- Read access to the repository. Every public repository accepts changes
  from any registered user; private ones from collaborators.

## Propose a change

Clone the repository you want to contribute to (not a fork) and commit on a
local branch:

```
git clone git@git.example:alice/proj.git
cd proj
git switch -c fix-feed-dates
# ... edit, commit ...
git push origin HEAD:refs/for/main
```

`refs/for/main` means "propose these commits for the `main` branch"; any
branch name works after `refs/for/`. The server answers:

```
remote: forge: created change 12 (v1, 2 commits) targeting main
remote: forge:   gemini://git.example/~alice/proj/changes/12
remote: forge:   next version: git push origin HEAD:refs/changes/12
 * [new reference]   HEAD -> refs/changes/12/v1
```

The title is the subject of your oldest new commit and the description is
that commit's message body. Options:

- `git push origin HEAD:refs/for/main -o title="Fix feed dates"` sets the
  title.
- `git push origin HEAD:refs/for/main -o topic=feed-dates` names the
  change; pushing again to `refs/for/main` with the same topic updates it
  instead of opening a new one.

Every plain push to `refs/for/<branch>` opens a **new** change. To update an
existing one, use its number (next section).

What is refused, with a message: a target branch that does not exist,
commits that are already in the target branch, more than 500 commits, a
push larger than 64 MiB, or more than 10 open changes of yours in one
repository.

## Update a change (push a new version)

Rewrite or extend your commits however you like (`git commit --amend`,
`git rebase -i`, new commits), then push to the change number:

```
git push origin HEAD:refs/changes/12
```

No `--force` is needed. Each push becomes a new **version** (`v2`, `v3`,
...). Earlier versions stay available; the change page shows every version,
the diff of each, and an *interdiff* between versions so reviewers see only
what you changed since their last look. A new version resets the review
state to *open*: earlier approvals and change requests are kept but marked
stale.

Shortcut: make `git push` do it for you on that branch.

```
git config branch.fix-feed-dates.remote origin
git config branch.fix-feed-dates.merge refs/changes/12
git config push.default upstream
```

If the target branch moved and you want to rebase:

```
git fetch origin main
git rebase origin/main
git push origin HEAD:refs/changes/12
```

Change the title or description: open the change page and follow the `edit`
action; upload the title on the first line, a blank line, then the
description. Lagrange loads the current text when you use the link (it uses
Titan's `;edit`).

## Read a change

`/~alice/proj/changes/` lists open changes (links for merged, closed, all).
`/~alice/proj/changes/12` is the change page:

- header: title, author, target branch, current version, state;
- links: `diff of v3`, `commits`, `patch (mbox)`, a fetch command, `feed`;
- **Versions**: one line per version with its diff, and `v2 -> v3
  interdiff` links;
- **Files**: per-file diff links for the current version;
- **Reviews** and **Comments**;
- **Merge** preview (maintainers): fast-forward, merge commit, or the list
  of conflicting files;
- footer: the actions you may perform.

Useful paths:

| Path | Shows |
| --- | --- |
| `/~alice/proj/changes/12/diff` | full diff of the current version |
| `/~alice/proj/changes/12/diff/<path>` | one file |
| `/~alice/proj/changes/12/v2/diff` | diff of version 2 |
| `/~alice/proj/changes/12/interdiff/2/3` | what changed between v2 and v3 (`git range-diff`) |
| `/~alice/proj/changes/12/commits` | the commits, oldest first |
| `/~alice/proj/changes/12/patch` | the series as an mbox for `git am` |
| `/~alice/proj/changes/12/feed` | subscribe to this change |
| `/~alice/proj/changes/feed` | subscribe to all changes of the repository |

Test it locally:

```
git fetch origin refs/changes/12/head
git checkout FETCH_HEAD
```

Or a specific version: `git fetch origin refs/changes/12/v2`.

## Review a change

Reviews and comments are Titan uploads: one text body per request. In
Lagrange, open the `review` or `comment` link in the change page's footer,
type or paste your text in the upload dialog, make sure your identity is
selected, and send. From a terminal, any Titan client works, for example
gmid's `titan(1)`:

```
titan -C cert.pem -K key.pem -m text/gemini \
  titan://git.example/~alice/proj/changes/12/review review.gmi
```

### Review

`titan://git.example/~alice/proj/changes/12/review`

The first non-empty line is the verdict, one of:

- `approve` - ready to merge (maintainers only; authors cannot approve their
  own change);
- `request-changes` - the author should push a new version (maintainers
  only);
- `comment` - feedback without a verdict (anyone who can read the
  repository).

The rest is your review, as gemtext.

### Comment

`titan://git.example/~alice/proj/changes/12/comment`

The whole body is the comment. Use it for discussion that is not a review.

### Pointing at a line: the `@` convention

There are no per-line comment boxes. Instead, start a section with an `@`
line naming the file and, optionally, the line and the version, and quote
the lines you are talking about with `> ` (gemtext quotes). The forge turns
the `@` line into links to that file's diff and blob.

```
approve

Looks good, two nits.

@ internal/web/titan.go:57
> 	if size > limit {
> 		return fmt.Errorf("too big")
Include the limit in the message so the client can show it.

@ docs/review-workflow.md v2
Typo in the second paragraph: "recieve".
```

Rules:

- `@ <path>` or `@ <path>:<line>`, optionally followed by ` v<k>`; the path
  is as shown in the diff (new name for renamed files); the line number
  counts lines of the new file. Without `v<k>`, the current version is
  assumed and recorded.
- A section runs until the next `@` line or the end of the body.
- Paste diff lines with a leading `> ` to quote them. Tabs and `+`/`-`
  prefixes may stay as they are.
- Everything else is ordinary gemtext. Headings are demoted and links from
  user text are marked `[user link]`, as everywhere on the forge.

### Review state

- *open*: no maintainer verdict on the current version;
- *changes requested*: a maintainer asked for changes on the current
  version;
- *approved*: at least one maintainer approved the current version and none
  is asking for changes;
- a new version returns the change to *open*;
- your latest review on a version replaces your earlier verdict on it.

## Merge a change (maintainers)

The change page's **Merge** section tells you what will happen: a
fast-forward, a merge commit, or conflicts. The `merge` link in the footer
is `titan://git.example/~alice/proj/changes/12/merge` with a one-time token.
Upload the merge commit message as the body (an empty body, `size=0`, uses
the default shown on the page):

```
Merge change #12: Fix feed dates

Dates were rendered in local time; feeds require UTC.
```

The forge appends a `Change: gemini://...` trailer and merges without a
working tree: fast-forward when the target has not diverged, otherwise a
merge commit authored by you and committed by the forge. The target branch
is updated atomically; if it moved while you were reading, you get "branch
moved, try again". On conflicts you get a page listing the conflicting files
and the rebase command for the author; nothing is changed.

Squash and rebase merges are deliberately absent: the author decides what
the commits look like by pushing versions.

## Close and reopen

- Author or maintainer: `titan://.../changes/12/close` with an optional
  reason (posted as a comment); `size=0` closes silently.
- `titan://.../changes/12/reopen` reopens if the target branch still
  exists. The author can also reopen simply by pushing a new version to
  `refs/changes/12`.
- Merged changes are final. To continue the work, push to `refs/for/main`
  again and open a new change.

## Offline and patch-based review

Anyone can download `/~alice/proj/changes/12/patch` (an mbox) and apply it
locally with `git am`, review in their editor, and upload the review body as
above. Uploading `git format-patch` output to create a change is planned for
a later version (ADR 0012, section 11); today the way in is `git push`.

## Permissions at a glance

| Action | needs |
| --- | --- |
| read, fetch `refs/changes/*`, download patches | read access (public: anyone) |
| propose (`refs/for/<branch>`), comment, review with `comment` | registered user with read access |
| push a new version | the change's author, or write access |
| `approve` / `request-changes`, merge | write access |
| edit title/description, close, reopen | author or write access |

## Troubleshooting

| Message from the server | Meaning |
| --- | --- |
| `you can only propose changes here` | you pushed to a branch or tag without write access; push to `refs/for/<branch>` instead |
| `target branch 'x' does not exist` | check `/~alice/proj/refs` for branch names |
| `nothing to review: <sha> is already in main` | your commits are already merged or you pushed the wrong ref |
| `already the current version` | the tip you pushed is unchanged |
| `change 12 is merged` | open a new change with `refs/for/<branch>` |
| `not the author of change 12` | only the author or a maintainer may push versions |
| `Conflicts in: ...` (merge page) | the author must rebase onto the target and push a new version |
| `branch moved, try again` | someone pushed to the target while you merged; reload and merge again |
