# Using git.as215520.net

git.as215520.net is a source forge you use from a Gemini client and from
git. Reading is Gemini; writing (issues, reviews, releases) is Titan, the
upload companion of Gemini; cloning and pushing is git over SSH. Your
identity is a client certificate you create in your own client, so there is
no password and no sign-up form. The service is served from several points
of presence at once (anycast) and works the same from all of them.

## What you need

- A Gemini client that supports client certificates and Titan uploads.
  Lagrange does both and is what these pages assume; gmid's `titan(1)` and
  any Gemini client work for the rest.
- `git` and `ssh`.

## Guide

- [Getting started](getting-started.md): browse, create your identity, register, add an SSH key, create a repository, push.
- [Accounts and certificates](accounts.md): what your certificate is, adding devices, revoking, SSH keys, your profile.
- [Git over SSH](git.md): clone and push URLs, verifying the host key, limits.
- [Repositories](repositories.md): creating, pages, settings, collaborators, visibility.
- [Issues](issues.md): opening, commenting, editing, closing.
- [Proposing and reviewing changes](changes.md): `refs/for/<branch>`, versions, reviews, merging.
- [Releases](releases.md): publishing a release and uploading assets.
- [Feeds](feeds.md): subscribing to activity, issues, changes, releases and incidents.
- [Titan uploads](titan.md): how writes work on this forge, with client notes.
- [Service status](status.md): the status page, incident reports and what to pin.

## Where things are

| Page | Path |
| --- | --- |
| Front page and recent activity | `/` |
| Your account | `/account` |
| A user | `/~user/` |
| A repository | `/~user/repo/` |
| Issues, changes, releases of a repository | `/~user/repo/issues/`, `/~user/repo/changes/`, `/~user/repo/releases/` |
| Service status | `/status/` |
| This guide | `/docs/` |

Everything on a public repository is public, including its issues, changes
and activity feeds. Private repositories and everything in them are
visible only to their owner, collaborators and the forge administrators.
