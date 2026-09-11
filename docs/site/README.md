# Using git.as215520.net

A source forge on Gemini. You read it with a Gemini client, write to it
(issues, reviews, releases) with Titan uploads from the same client, and
clone and push with git over SSH. Your identity is a client certificate;
there is no password.

You need a Gemini client with client certificates and Titan (Lagrange
does both), plus `git` and `ssh`.

- [Getting started](getting-started.md): identity, account, SSH key, first repository.
- [Repositories](repositories.md): pages, settings, collaborators.
- [Issues](issues.md)
- [Proposing changes](changes.md): `git push` to `refs/for/<branch>`, review, merge.
- [Releases](releases.md)
- [Feeds](feeds.md): subscribe to anything.
- [Service status](status.md): the status page, incidents, and the fingerprints to trust.

Everything in a public repository is public. Private repositories are
seen only by their owner and collaborators.
