# Git over SSH

## URLs

```
git@git.as215520.net:user/repo.git
ssh://git@git.as215520.net/user/repo.git
```

Both forms work; `.git` is optional. Public repositories can be cloned by
anyone with a registered key; private ones by their owner and
collaborators. Pushing needs write access. The login name is ignored (use
`git@`), the key you offer identifies you (see
[Accounts and certificates](accounts.md)).

## Verify the host key

The server has one ed25519 host key, the same on every point of presence.
On your first connection ssh shows its fingerprint; it must be:

```
SHA256:0pNfPYIMlNWpteYvSZp5AHaXiLtnl8roTxOzsUTrJOg
```

It is also published in DNS as an SSHFP record (`dig SSHFP
git.as215520.net`), so `ssh -o VerifyHostKeyDNS=yes` checks it for you. The
key changes only by announced rotation, listed on the [status page](status.md).

## What the server does and does not do

The SSH service exists to run `git-upload-pack` (clone, fetch) and
`git-receive-pack` (push), nothing else: no shell, no SFTP, no port or
agent forwarding. Git protocol version 2 is used when your git asks for
it. A session may last up to 30 minutes and is closed after 5 minutes
without traffic.

Because the service is anycast, your push may reach a point of presence
that is not the one holding the repository's writable copy. That is
handled for you: the push is relayed to the right node and behaves exactly
as a direct one, including any message from the server-side checks.

## Limits

| What | Limit |
| --- | --- |
| One push | 1 GiB |
| One repository | 2 GiB |
| All your repositories together | 10 GiB |
| Repositories per account | 100 |

Larger pushes are refused with a message before anything is written.

## Messages you may see

| Message | Meaning |
| --- | --- |
| `forge: repository 'user/repo' not found` | it does not exist, or it is private and your key has no access |
| `forge: forbidden: write access to 'user/repo' denied` | you can read but not push; see [changes](changes.md) to propose commits instead |
| `you can only propose changes here` | same, on a push to a branch: push to `refs/for/<branch>` |
| `forge: interactive shell not available; use git` | you ran `ssh git@git.as215520.net` without a git command |
| `forge: session timed out` | the 30-minute cap; retry with a smaller push |
