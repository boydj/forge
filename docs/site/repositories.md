# Repositories

`/~user/repo/` is the overview: clone command, branches, latest commits
and the README. From there: *files* (browse any branch, tag or commit;
Markdown and gemtext render, everything else is shown as text or served
raw), *history*, *branches and tags*, *issues*, *changes*, *releases* and
*feed*.

## Settings

Owners have a *settings* link on the overview page:

- description;
- visibility: public, or private (hidden from everyone but the owner and
  collaborators);
- default branch;
- archive (read-only for everyone) and unarchive;
- collaborators: add a user as `read`, `write` or `admin`, or remove one.
  Writers can push, merge and manage issues and releases; admins can also
  change settings;
- delete the repository (type its name to confirm).

## Cloning and pushing

```
git clone git@git.as215520.net:user/repo.git
```

Public repositories can be cloned by any registered key; pushing needs
write access. If you can read a repository but not push to it, propose
your commits instead: see [Proposing changes](changes.md).
