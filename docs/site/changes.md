# Proposing changes

A change is a set of commits you propose for a branch. You create it with
`git push`, discuss it on its page, update it by pushing again, and a
maintainer merges it. No forks.

## Propose

Clone the repository, commit on a branch, and push to `refs/for/<branch>`:

```
git push origin HEAD:refs/for/main
```

The server prints the change number and its page,
`/~user/repo/changes/<n>`. The title comes from your first commit
(`-o title="..."` to set one).

## Update

Amend or rebase as you like, then push to the change number; no `--force`:

```
git push origin HEAD:refs/changes/<n>
```

Every push is a new version. The page keeps every version and shows what
changed between them.

## Review

On the change page, *review* and *comment* are uploads. A review starts
with a verdict on its first line, `approve`, `request-changes` or
`comment`, followed by your text. To point at a line, start a section
with `@ path:line` and quote the lines with `> `:

```
approve
Two nits.
@ src/feed.go:57
> 	if size > limit {
Say what the limit is.
```

Try a change locally with `git fetch origin refs/changes/<n>/head`.

## Merge

Maintainers see a *Merge* section on the change page saying whether it
fast-forwards, needs a merge commit, or conflicts. Follow *merge* and
upload the merge message (or an empty upload for the default).

Close a change with *close*; pushing a new version reopens it.
