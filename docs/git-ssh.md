# Git over SSH

Package: `internal/sshd`. Threat model: `docs/threat-model.md` T-04, T-05,
T-11, T-18, T-36 and invariants I-1..I-3. Decisions: ADR 0001, 0004, 0005, 0009.

The forge speaks SSH for exactly one purpose: running `git-upload-pack`
(clone, fetch) and `git-receive-pack` (push) on a repository the
authenticated account may access. There is no shell, no PTY, no SFTP, no
port, agent or X11 forwarding and no environment passthrough. The server is
`golang.org/x/crypto/ssh` driven directly; nothing from OpenSSH's `sshd` is
involved and the process never consults `authorized_keys` files.

## Transport walkthrough

1. **TCP accept.** Global and per-source-IP connection caps are checked
   before any bytes are read (`Config.MaxConns`, `Config.MaxConnsPerIP`;
   the `Limits.ssh_max_conns*` keys in `forge.toml`). Refused connections are
   closed silently.
2. **Handshake** under an absolute deadline (`Config.HandshakeTimeout`,
   default 20 s) covering banner, key exchange and authentication. The
   server announces `SSH-2.0-forge`, presents a single ed25519 host key and
   offers only the algorithms below.
3. **Authentication: public key only.** No password, keyboard-interactive,
   GSSAPI or "none". The login name is ignored (clone URLs use `git@`, but
   any name works). The offered key is passed to the `Authenticator`, which
   maps it to an account or returns `ErrUnknownKey`. Certificates
   (`*-cert-v01@openssh.com`) and RSA keys shorter than 3072 bits are refused
   before the lookup. Every failure is reported identically to the client.
   `MaxAuthTries` (default 6, `ssh.max_auth_tries`) bounds attempts per
   connection.
   The account is stored in `ssh.Permissions.Extensions` by the callback and
   read back from `ServerConn.Permissions`, i.e. from the key that actually
   completed authentication, never from the username (invariant I-3).
4. **Channel loop.** Only `session` channels are accepted; `direct-tcpip`,
   `forwarded-tcpip`, `x11`, `auth-agent@openssh.com` and anything else are
   rejected with `Prohibited`. All global requests (`tcpip-forward`,
   `cancel-tcpip-forward`, keepalives) are answered with failure. At most
   four session channels may be open per connection (OpenSSH `ControlMaster`
   multiplexing opens several).
5. **Session requests.**
   - `env`: only `GIT_PROTOCOL` with the exact value `version=2` is kept;
     every other name or value is refused and discarded.
   - `exec`: exactly one per session. The command line is parsed with the
     grammar below, authorised, and git is started. A second `exec` is refused.
   - `shell`: refused; the client sees
     `forge: interactive shell not available; use git` on stderr and exit
     status 1.
   - `pty-req`, `subsystem` (including `sftp`), `x11-req`,
     `auth-agent-req@openssh.com`, `signal`, `window-change`, `break`: refused.
6. **Execution.** The whole session runs under `Config.SessionTimeout`
   (default 30 min, `ssh.session_timeout`); on expiry the git process is
   killed and the client sees `forge: session timed out`. The TCP connection
   carries a rolling `Config.IdleTimeout` (default 5 min, `ssh.idle_timeout`)
   on every read and write after the handshake, and the same value is passed
   to `upload-pack --timeout`. Channel stdin is piped to git's stdin (client
   EOF closes it), git's stdout and stderr go to the channel's data and
   extended-data streams, and when git exits the server sends `exit-status`
   with its code, EOF, and closes the channel.
7. **Logging and metrics.** One structured log line per session
   (`remote`, `account`, `key` fingerprint, `op`, `repo`, `exit`, `ms`,
   `timeout`, `protocol`), plus `Metrics.ObserveSession(op, success, d)` and
   `Metrics.ConnectionsChanged(±1)`.

## Command grammar

The exec payload must match exactly:

```
command := verb SP path
verb    := "git-upload-pack" | "git-receive-pack" | "git upload-pack" | "git receive-pack"
path    := ["'"] ( "~" owner | ["/"] owner ) "/" repo [".git"] ["'"]
owner   := [a-z][a-z0-9-]{0,31}
repo    := [a-z0-9][a-z0-9._-]{0,63}       (not ending in ".git")
```

Accepted examples (all resolve to `alice/proj`):

```
git-upload-pack 'alice/proj.git'      git receive-pack 'alice/proj'
git-upload-pack /alice/proj.git       git-receive-pack '~alice/proj.git'
git-upload-pack alice/proj
```

Rules, in the order they are checked:

- Non-empty, at most 256 bytes, printable ASCII only (no control bytes, no
  UTF-8).
- One of the four verbs followed by exactly one space. `git-upload-archive`
  is recognised only to produce a clear "not supported" message.
- Single quotes, if present, must enclose the whole path. Double quotes are
  not accepted.
- After unquoting, the path may contain only `a-z 0-9 - . _ / ~`. This
  rejects spaces, further arguments, `;`, `|`, `&`, `$()`, backticks and
  option-looking tokens before any structure is examined.
- One leading `~` (directly followed by the owner) or one leading `/`.
- Exactly two path segments; `..`, empty segments, `//`, trailing `/` and
  extra segments are rejected.
- `.git` is stripped once from the repository segment, then owner and repo
  are validated against the regular expressions above (ADR 0009). Names are
  case-sensitive lowercase.

The line is never shell-split and never passed to a shell. The disk path
comes from the `Authorizer`, not from the client; argv is built as
`git upload-pack --strict --timeout=<idle seconds> -- <path>` or
`git receive-pack -- <path>` (git 2.43 accepts `--` for both). Any parse
failure yields exit status 1 and a `forge: ...` line on stderr.

## Authorisation

`Authorizer.Authorize(ctx, account, owner, repo, op)` returns the bare
repository path or:

- `ErrNoRepo` when the repository does not exist **or** is private and not
  readable by the account. The client sees
  `forge: repository 'owner/repo' not found` in both cases (T-10: existence
  is not leaked).
- `ErrForbidden` when the account can read but not write (push). The client
  sees `forge: forbidden: write access to 'owner/repo' denied`.

Any other error is logged and reported as `forge: internal error`.

## Environment passed to git and hooks

`git.Backend.Env` supplies the hardened base environment (ADR 0004:
`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`, `core.hooksPath`,
fsck settings, `protocol.*.allow=never`, ...). The SSH server adds:

| Variable | Value |
| --- | --- |
| `FORGE_ACCOUNT_ID` | numeric account id of the authenticated key's owner |
| `FORGE_ACCOUNT` | account name |
| `FORGE_REPO` | `owner/repo` as validated (no `.git`) |
| `FORGE_HOOK_SOCKET` | path of the daemon's hook Unix socket (`Config.HookSocket`) |
| `FORGE_OP` | `upload` or `receive` |
| `FORGE_REMOTE_IP` | client IP address (for audit only) |
| `GIT_PROTOCOL` | `version=2`, only if the client requested it via `env` |

Server-side hooks (`pre-receive`, `update`, `post-receive`, run as
`forge hook <name>` through `core.hooksPath`) read `FORGE_*` to identify the
pusher and repository and talk to the daemon over `FORGE_HOOK_SOCKET`;
they must not trust anything else in their environment or in the
repository.

## Security properties

- I-1: no shell, PTY, subsystem, port/agent/X11 forwarding, ever. Every
  such request is refused at the protocol level; there is no code path that
  could grant one.
- I-2: only `git-upload-pack` / `git-receive-pack` run, with a path derived
  solely from a validated `owner/repo` pair returned by the authoriser.
- I-3: identity comes from the authenticated key, stored in
  `Permissions.Extensions` and read from `ServerConn.Permissions`, never from
  the username.
- Environment: the only client-controlled environment value that reaches git
  is the literal `GIT_PROTOCOL=version=2`.
- Algorithms: KEX `mlkem768x25519-sha256`, `curve25519-sha256`
  (+`@libssh.org`), `ecdh-sha2-nistp256/384/521`; ciphers
  `chacha20-poly1305@openssh.com`, `aes256/128-gcm@openssh.com`,
  `aes256/128-ctr`; MACs `hmac-sha2-256/512-etm@openssh.com`,
  `hmac-sha2-256/512`; client keys `ssh-ed25519`,
  `sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com`,
  `ecdsa-sha2-nistp256/384/521`, `rsa-sha2-256/512` (RSA >= 3072 bits).
  No `ssh-rsa` (SHA-1) signatures, no `ssh-dss`, no CBC, no `hmac-sha1`,
  no DH-group1/14-sha1. Host key: ed25519 only (`LoadOrCreateHostKey`
  refuses other types).
- Resource bounds: handshake, session and idle timeouts; global and per-IP
  connection caps; per-connection session cap; git subprocess concurrency
  slots (`git.Backend.Acquire`, 10 s queue then "server busy");
  `exec.Cmd.WaitDelay` so a stuck pipe cannot hold a process forever;
  session requests are serviced concurrently with the transfer so an
  unanswered request can never stall the connection.
- Auth failures are indistinguishable to the client (unknown, revoked,
  wrong key type, certificate, undersized RSA all yield the same failure)
  and are logged at debug level with the source IP only.

## Host key and DNS (SSHFP)

`sshd.LoadOrCreateHostKey(path)` loads `ssh.host_key_file` (default
`<data_dir>/ssh/host_ed25519`) or generates an ed25519 key with mode 0600 in
OpenSSH format and writes `<path>.pub` beside it. Every POP presents the same
key (anycast; see T-36 for custody and rotation).

`sshd.SSHFPRecords(pub)` returns the RDATA for two DNS records:

```
git.example.net. IN SSHFP 4 1 <sha1 hex>
git.example.net. IN SSHFP 4 2 <sha256 hex>
```

(algorithm 4 = Ed25519, 1 = RSA, 3 = ECDSA; fingerprint type 1 = SHA-1,
2 = SHA-256). The same output is produced by `ssh-keygen -r git.example.net
-f host_ed25519.pub`. Publish them in the DNSSEC-signed zone; clients with
`VerifyHostKeyDNS yes` (or `ask`) then verify the host key on first contact
without a TOFU prompt. The SHA-256 fingerprint
(`ssh-keygen -lf host_ed25519.pub`) is also shown on the Gemini front page.

## Client configuration notes

- Clone URLs: `git@git.example.net:owner/repo.git` (scp-like) or
  `ssh://git@git.example.net/owner/repo.git`. `~owner/repo` and a missing
  `.git` are also accepted.
- **Non-22 ports** must use the `ssh://` form:
  `ssh://git@git.example.net:2222/owner/repo.git`. The scp-like syntax has
  no port field (`git@host:2222/owner/repo` would be parsed as a path).
  Alternatively put `Port 2222` in `~/.ssh/config` for the host.
  `config.CloneURL` generates the right form from `ssh.port`.
- Protocol v2 is used automatically by modern git (`protocol.version`
  defaults to 2); git passes `-o SendEnv=GIT_PROTOCOL` to OpenSSH and the
  server honours that single variable. Force it with
  `git -c protocol.version=2 clone ...`; confirm with
  `GIT_TRACE_PACKET=1` (look for `version 2`).
- Recommended `~/.ssh/config` stanza:

  ```
  Host git.example.net
      User git
      IdentityFile ~/.ssh/id_ed25519_forge
      IdentitiesOnly yes
      VerifyHostKeyDNS yes
      # Port 2222   # only if the forge is not on 22
  ```

- `ssh git@git.example.net` (no command) prints the shell refusal and exits
  non-zero; this is expected and is the quickest way to check that a key is
  registered (an unregistered key gets `Permission denied (publickey)`).
- The same private key must not be used as both a TLS client-certificate
  identity and an SSH key (ADR 0005).

## Testing

`go test -race ./internal/sshd` starts the server on `127.0.0.1:0` with an
in-memory authenticator/authoriser and a real bare repository, then drives
the system `git` and `ssh` binaries through `GIT_SSH_COMMAND` (clone with v2
and v0, push, fetch, unknown key, password auth, read-only push, missing
repo, shell, non-git exec, traversal, `-R`/`-L` forwarding, `sftp`
subsystem) and the Go SSH client (channel/request refusals, second exec,
session timeout, connection limits, handshake timeout). The `ssh`-based
tests skip when the binary is absent. `ParseCommand` has a table test with
valid forms and injection, option, traversal, unicode and length cases.

## Notes learned in production

- Repositories are initialised **without** `core.sharedRepository`. The forge
  user owns every repo and no group sharing is wanted; setting it makes git
  add the setgid bit to new directories with `chmod`, which the hardened
  `forge.service` (`RestrictSUIDSGID=yes`) blocks, breaking reflog directory
  creation on the first push. `forge admin maintenance` unsets it on any
  older repository.
- Under anycast, a `git push` can land on a replica. The replica relays the
  whole receive-pack session to the repository's leader over the control
  plane (single-writer, ADR 0011; see `docs/replication.md`, Forwarding):
  the client sees the leader's hook output and exit status as if it had
  pushed there, and the replica catches up through normal replication
  within a second. Only when the leader cannot be reached is the push
  refused: `forbidden: pushes for this repository are accepted by node
  <leader> (leader unreachable)`. Retrying later, or pushing to the leader's
  unicast name (`<leader>.nodes.<zone>`, same host key), then works once
  the leader is back. Fetches and clones are always served locally.
- Push forwarding in the code: `internal/sshd` `Server.ServeGit` is the one
  path that authorises and runs a transport command; a local session and a
  forwarded push (`Server.ServeForwardedPush`, reached through
  `/v1/forward/receive-pack`) both go through it, so hooks, quotas, the
  hardened environment and the identity exported to `forge hook` are the
  same either way. `forge_ssh_push_forwards_total{role,result}` counts
  relays on both ends.
