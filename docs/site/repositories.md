# Repositories

## Creating one

`/new` asks for a name: lowercase letters, digits, `.`, `_` and `-`, up to
64 characters, starting with a letter or digit; `feed`, `new` and `edit`
are reserved and a name cannot end in `.git`. The repository is created
empty and public, with `main` as its default branch, at `/~you/name/`.
Push to it as shown in [Git over SSH](git.md).

## Pages

| Page | Path |
| --- | --- |
| Overview: clone command, branches, latest commits, README | `/~user/repo/` |
| Files at a branch, tag or commit | `/~user/repo/tree/<ref>/` and `/~user/repo/tree/<ref>/<path>` |
| One file rendered (text, Markdown, gemtext) | `/~user/repo/tree/<ref>/<path>` |
| One file raw, with its detected type | `/~user/repo/raw/<ref>/<path>` |
| History of a ref or of a path | `/~user/repo/log/<ref>` and `/~user/repo/log/<ref>/<path>` |
| One commit with its diff | `/~user/repo/commit/<id>` |
| Branches and tags | `/~user/repo/refs` |
| Issues, changes, releases | `/~user/repo/issues/`, `/~user/repo/changes/`, `/~user/repo/releases/` |
| Activity feed | `/~user/repo/feed` |

A `README.gmi` or `README.md` at the repository root renders on the
overview page (gemtext first if both exist). Markdown renders as gemtext:
headings, paragraphs, lists, quotes, fenced code and links, with links
hoisted after their paragraph and marked `[readme link]`. Relative links
resolve within the repository. Files larger than 512 KiB are shown
truncated with a link to the raw copy.

## Settings

Owners (and collaborators with the admin role) have a *settings* link on
the overview page:

- **description**, shown in listings and feeds;
- **visibility**: public or private. A private repository, its issues,
  changes, releases and events are hidden from everyone but its owner,
  collaborators and administrators; its name is not revealed either;
- **default branch**, the one shown first and targeted by the clone
  command;
- **archive or unarchive**: an archived repository is read-only for
  everyone, including its owner (no pushes, issues or changes);
- **collaborators**: add a user with the `read`, `write` or `admin` role, or
  remove one;
- **delete**: removes the repository and everything in it; type its name
  to confirm.

Roles: *read* sees a private repository and can propose changes; *write*
can push, merge changes, manage issues and releases; *admin* can also
change settings and collaborators. Everyone can read a public repository.

Settings changes are typed at prompts (they are Gemini INPUT actions that
ask you to confirm), so a link alone never changes anything.
