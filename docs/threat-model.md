# Threat Model

Status: draft v0.1 (pre-implementation). Owner: Security. Review cadence: every milestone boundary and after any incident.

This document describes what we are protecting, who we are protecting it from, and what the code and infrastructure must do about it. Everything in the "Mitigations" columns is intended to be implementable as written; where a mitigation names a git config key, a systemd directive, or a BIRD filter, that is the exact knob to set.

Scope: the forge daemon (Gemini, Titan, SSH/Git, hooks, SQLite, feeds, admin CLI, metrics), its multi-POP anycast deployment (BGP/BIRD, WireGuard mesh, leader/replica replication), secrets handling (SOPS+age), infrastructure-as-code (OpenTofu on Vultr + Cloudflare DNS), and the software supply chain.

Out of scope: client software (Gemini browsers, git clients), the security of users' own machines, and volumetric attacks larger than our upstream capacity (we can only absorb and degrade gracefully).

---

## 1. Assets

| ID | Asset | Why it matters | Confidentiality / Integrity / Availability |
|----|-------|----------------|---------------------------------------------|
| A1 | Git object stores (bare repos on disk) | The product. History integrity is the whole value proposition. | I, A (C for private repos) |
| A2 | Refs (branches, tags) and their ACLs | Who may write where. A ref forge is a supply-chain attack on every downstream user. | I |
| A3 | SQLite metadata DB (accounts, cert fingerprints, SSH keys, repo ACLs, issues, comments, reviews, releases, tokens) | Identity and authorization root; also user content. | C, I, A |
| A4 | Release assets (binaries uploaded via Titan) | Executed by users; tampering = malware distribution. | I, A |
| A5 | User identity material on our side: TLS client cert fingerprints (SHA-256 of DER), SSH public keys, recovery bindings | Compromise = impersonation. | I |
| A6 | TLS server private key (Gemini/Titan, port 1965) | TOFU means users pin this key; compromise = silent MITM on every POP. | C |
| A7 | SSH host key(s) | Same as A6 for git over SSH. | C |
| A8 | WireGuard node private keys | Membership in the replication mesh. | C |
| A9 | age private key(s) for SOPS | Decrypts every other secret. | C |
| A10 | BGP session credentials (MD5/TCP-AO passwords), RPKI ROA signing authority, RIR (RIPE) account for AS215520 | Control of the anycast prefix announcements. | C, I |
| A11 | Vultr API token, Cloudflare API token, OpenTofu state | Control of every VM and DNS record. | C, I |
| A12 | Availability of read path (Gemini browse, git clone) | Public users depend on it; anycast is the main defence. | A |
| A13 | Availability of write path (leader per repo) | Single leader per repo means leader loss = write outage for that repo. | A |
| A14 | Audit/event log (hook emissions, admin actions) | Needed to detect and recover from everything above. | I, A |
| A15 | Build artifacts of the forge itself (release binaries, container images) | Supply chain for operators. | I |
| A16 | Operator reputation / legal standing | Illegal content hosted under our AS and domain. | - |

---

## 2. Trust boundaries

```
                          INTERNET (untrusted)
   ┌───────────────────────────────────────────────────────────────────────┐
   │  Anonymous Gemini readers   Cert-holding users   git/ssh clients      │
   │  BGP peers / upstreams      Attackers            Cloudflare DNS       │
   └───────────┬───────────────────────┬─────────────────────┬─────────────┘
               │ TLS 1.2+/1965         │ TLS+Titan/1965      │ SSH/22|2222
   ════════════╪═══════════════════════╪═════════════════════╪════ TB1: network edge (anycast /24, /48)
               ▼                       ▼                     ▼
   ┌───────────────────────────────────────────────────────────────────────┐
   │ POP node (Vultr VM, one of N)          systemd hardened unit         │
   │ ┌────────────────────────────────────────────────────────────────┐   │
   │ │ forged (single Go daemon)                                       │   │
   │ │  ┌──────────┐ ┌──────────┐ ┌──────────────┐ ┌──────────────┐    │   │
   │ │  │ Gemini   │ │ Titan    │ │ SSH (exec    │ │ Admin CLI    │    │   │
   │ │  │ handler  │ │ handler  │ │ only)        │ │ (unix sock)  │    │   │
   │ │  └────┬─────┘ └────┬─────┘ └──────┬───────┘ └──────┬───────┘    │   │
   │ │       │  TB2: request parsing / identity establishment          │   │
   │ │  ═════╪════════════╪══════════════╪════════════════╪═══════     │   │
   │ │       ▼            ▼              ▼                ▼            │   │
   │ │  ┌──────────────────────────────────────────────────────────┐   │   │
   │ │  │ authz core: (identity, repo, action) -> allow/deny        │   │   │
   │ │  └───────┬──────────────────────────────┬────────────────────┘   │   │
   │ │          │ TB3: authz -> storage        │ TB4: daemon -> git subprocess
   │ │  ════════╪══════════════════════════════╪═════════════════      │   │
   │ │          ▼                              ▼                       │   │
   │ │  ┌──────────────┐               ┌──────────────────────────┐    │   │
   │ │  │ SQLite (WAL) │               │ git-upload-pack /         │    │   │
   │ │  │ metadata     │               │ git-receive-pack          │    │   │
   │ │  └──────────────┘               │ (sandboxed, ulimits, -c)  │    │   │
   │ │                                 └──────────┬───────────────┘    │   │
   │ │                                            ▼ TB5: hooks         │   │
   │ │                                 ┌──────────────────────────┐    │   │
   │ │                                 │ bare repos on disk        │    │   │
   │ │                                 │ /var/lib/forge/repos      │    │   │
   │ │                                 │ hooks -> fixed safe dir   │    │   │
   │ │                                 └──────────────────────────┘    │   │
   │ └────────────────────────────────────────────────────────────────┘   │
   │  Prometheus /metrics (private addr only)   SOPS+age secrets (RAM)    │
   │  BIRD (BGP to upstream)                    WireGuard (wg0)           │
   └────────────────────────────┬───────────────────────────┬────────────┘
                                │ eBGP                      │ WG mesh (per-node keys)
   ═════════════════════════════╪═══════════════════════════╪════ TB6: inter-node
                                ▼                           ▼
                   ┌──────────────────┐        ┌────────────────────────────┐
                   │ Upstream / IX    │        │ Other POPs                 │
                   │ (Vultr BGP)      │        │  leader(repo X) <-> replica│
                   └──────────────────┘        │  forwarded Titan/push      │
                                               │  git fetch replication     │
                                               └────────────────────────────┘
   ═══════════════════════════════════════════════════════════════ TB7: control plane
   ┌───────────────────────────────────────────────────────────────────────┐
   │ Operator laptop(s): OpenTofu, SOPS/age keys, Vultr+Cloudflare tokens, │
   │ RIPE/RPKI portal, admin CLI over SSH                                  │
   └───────────────────────────────────────────────────────────────────────┘
   ═══════════════════════════════════════════════════════════════ TB8: supply chain
   ┌───────────────────────────────────────────────────────────────────────┐
   │ Go toolchain, Go modules (go.sum), git binary, OS packages, CI,       │
   │ container base images, Vultr images                                   │
   └───────────────────────────────────────────────────────────────────────┘
```

Boundary definitions:

- TB1 network edge: everything arriving on 1965/22/2222 is hostile bytes. TCP-level flooding is partially outside our control; anycast spreads it.
- TB2 parsing and identity: request line parsing (Gemini URL, Titan parameters, SSH exec command) and identity establishment (cert fingerprint lookup, SSH key lookup). No user string crosses TB2 without canonicalization and validation.
- TB3 authz to storage: the only path to SQLite and the repo tree is through the authz core. Handlers never touch storage directly.
- TB4 daemon to git subprocess: git is treated as a semi-trusted, fragile program processing hostile input. It runs with a fixed, fully specified configuration and resource limits, never reading config from the repository or the environment.
- TB5 hooks: hooks are our code (compiled into the daemon, invoked via a fixed hooks path), never repository content.
- TB6 inter-node: replicas are lower-trust than leaders. A replica can be compromised without compromising the write authority for repos it does not lead. All inter-node traffic is over WireGuard only.
- TB7 control plane: operator machines and cloud accounts. Highest value, lowest exposure; protected by key custody and MFA, not by our code.
- TB8 supply chain: code we did not write but execute.

---

## 3. Actors

| Actor | Capabilities | Trust level | Goals (if malicious) |
|-------|--------------|-------------|----------------------|
| Anonymous Gemini reader | Open TLS to 1965 with or without a client cert; send a URL; clone public repos over SSH? (No: SSH always requires a key. Anonymous clone is via `git://`-less; public clone is Gemini-only or SSH with a registered key. See T-22.) | None | DoS, enumeration, scraping, content injection via crafted URLs |
| Authenticated user | Has a registered client cert and/or SSH key; can create repos, issues, comments, push to own repos, participate in reviews | Low (self-registered, TOFU) | Spam, abuse, resource exhaustion, privilege escalation, attacks on other users via content |
| Repo owner | Authenticated user with admin rights on specific repos; manages collaborators, ACLs, releases, hooks settings | Low but scoped | Attack the server via their own repo (malicious objects, huge repos), host illegal content |
| Admin | Runs the admin CLI, holds SOPS/age keys, deploys | High | Insider threat; more realistically, the target of phishing/credential theft |
| Replica node | Holds a WireGuard key, a copy of public repo data, and serves reads; forwards writes | Medium | If compromised: serve tampered reads, forge forwarded writes, pivot to leader |
| Leader node (per repo) | Accepts writes for the repos it leads; holds the authoritative copy | Medium-high | If compromised: full write control over led repos |
| Upstream network / BGP peers | Route our prefixes; can hijack, leak, blackhole, or observe traffic | Untrusted for integrity; TLS and SSH provide end-to-end protection | Hijack for MITM (defeated by TOFU pins + SSH host keys, if clients verify), blackhole for DoS |
| Provider (Vultr, Cloudflare) | Physical access to VMs and disks; control over DNS | Trusted by necessity; treat as "can read disk, can reboot" | Subpoena compliance, insider misuse, provider compromise |
| Supply chain | Go modules, toolchain, git binary, OS packages, CI runners | Untrusted until pinned and verified | Backdoor the daemon or the build |

---

## 4. Threats

Likelihood scale: Low / Medium / High. Impact scale: Low / Medium / High / Critical. Every threat lists mitigations that are expected to be implemented; "M-n" milestone references are resolved in section 5.

### 4.1 Git transport and object store

#### T-01 Malicious push: ref forgery / unauthorized ref update
- Description: A user with push access to some refs (or none) updates refs they should not, e.g. rewrites `main`, deletes tags, or pushes to a protected branch.
- Attack scenario: Collaborator with "push to feature/*" role sends a `git-receive-pack` command list including `refs/heads/main` and `refs/tags/v1.0` updates.
- Affected: SSH server, receive-pack, pre-receive hook, ACL.
- Impact: High (supply-chain effect for downstream users).
- Likelihood: High (trivial to attempt).
- Mitigations:
  - Authorization is evaluated per ref in `pre-receive` (all refs in one invocation, reject all if any fails) and again in `update` as belt-and-braces. Never rely on client-side or `git-receive-pack` defaults.
  - Protected refs (`main`/default branch, `refs/tags/*`) require role >= maintainer; force-push (non-fast-forward) and deletion on protected refs require owner and are logged. Implement as: pre-receive computes `git merge-base --is-ancestor old new` and refuses if not fast-forward unless the actor is allowed.
  - `receive.denyDeletes=true`, `receive.denyNonFastForwards=true` set as *baseline* via `git -c` for every receive-pack; hooks then selectively permit.
  - `receive.denyCurrentBranch=refuse` (irrelevant for bare, but set anyway).
  - `receive.updateServerInfo=false`, `receive.advertiseAtomic=true`, `receive.advertisePushOptions=false` (until push options are needed and validated).
  - Hidden refs: `receive.hideRefs` and `uploadpack.hideRefs` for `refs/forge/*` (internal metadata refs) so users cannot see or update them; additionally reject any pushed ref not matching `^refs/(heads|tags)/` in pre-receive.
  - Ref name validation server-side: `git check-ref-format --branch` semantics plus reject names containing control chars, `..`, `@{`, leading `-`, or non-ASCII.

#### T-02 Malicious Git object graphs (fsck-failing objects, dangerous tree entries)
- Description: Pushed packs contain objects that git tools mishandle: `.git` directory entries in trees, `.gitmodules` pointing at symlinks (CVE-2024-32002 class), NTFS/HFS aliases (`GIT~1`, `.git‌`), zero-padded modes, duplicate tree entries, tree entries with `/` in names, bad tag/commit headers.
- Attack scenario: Attacker pushes a repo that clones fine on our server but executes code on a developer's Windows/macOS machine on `git clone --recursive`; or a tree with `.git/hooks/post-checkout` entry that triggers via case-insensitive FS.
- Affected: receive-pack, replicas (fetch), any developer cloning.
- Impact: High (RCE on users' machines; we would be the distribution vector).
- Likelihood: Medium.
- Mitigations:
  - `receive.fsckObjects=true`, `transfer.fsckObjects=true`, `fetch.fsckObjects=true` (replicas) set via `git -c` on every invocation. Do not soften with `fsck.<msg-id>=ignore` except for a documented, reviewed allowlist (start empty).
  - `core.protectNTFS=true`, `core.protectHFS=true` on every git invocation regardless of host OS.
  - Explicit pre-receive tree scan for pushed commits (new objects only, via `git rev-list --objects new ^old | git cat-file --batch-check`): reject any tree entry named `.git` (any case), any entry matching `^\.git(~|\.)` NTFS short names, any entry containing `\\`, and any symlink entry named `.gitmodules`, `.gitattributes`, or `.gitignore` in root. Reject `.gitmodules` whose `path` or `url` values contain `..`, absolute paths, or `-` prefix (option injection).
  - Reject submodule additions pointing to `file://`, `ext::`, or local paths.
  - `git fsck --strict --no-dangling` scheduled on all repos (leaders and replicas) with results in metrics/alerts.

#### T-03 `.gitattributes` / `.gitconfig` / hooks executing server-side
- Description: Git features that run user-controlled code or read user-controlled config: clean/smudge filters, `core.hooksPath`, `include.path`, `safe.directory` bypass, `diff.<driver>.command`, `core.fsmonitor`, `core.sshCommand`, `core.pager`, `core.editor`.
- Attack scenario: Attacker commits `.gitattributes` with `* filter=lfs` and hopes a server-side diff or archive invocation applies a filter; or the daemon runs git with cwd inside a user-controlled tree containing `.git/config`.
- Affected: All server-side git invocations (diff rendering, archive, blame, log).
- Impact: Critical (RCE as the daemon user).
- Likelihood: Medium (well known; easy to slip in as the feature set grows).
- Mitigations:
  - Every git subprocess: `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `HOME=/nonexistent`, `XDG_CONFIG_HOME=/nonexistent`, `GIT_ATTR_NOSYSTEM=1`, `GIT_CEILING_DIRECTORIES` set to the repo root's parent, `GIT_DIR` set explicitly (never rely on discovery), cwd set to an empty scratch directory, not the repo.
  - Pass all config via `git -c key=value` (or `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n`), never a config file inside the repo. On repo creation, write a minimal `config` and never let users edit it (no Titan endpoint exposes it). Treat repo `config` as ours; verify hash on startup/scrub.
  - `core.hooksPath=/usr/lib/forge/hooks` (immutable, root-owned, shipped with the daemon). The daemon verifies this value on every receive-pack via `-c core.hooksPath=...` so a modified repo config cannot override it.
  - Server-side operations never check out a worktree. Diff/blame/archive use `git diff-tree`, `git cat-file`, `git archive` on bare repos with `-c core.attributesFile=/dev/null`, `-c diff.external=`, `-c diff.*.command` unset, and `--no-textconv --no-ext-diff`. `git archive` runs with `-c tar.*.command=` cleared and `--worktree-attributes` never passed; export-subst/export-ignore attributes are honored only if we deliberately decide so (default: `-c core.attributesFile=/dev/null` disables).
  - `safe.directory`: run git as the same uid that owns the repos so the check passes without `safe.directory=*`. Never set `safe.directory=*`.
  - `protocol.allow=never` plus `protocol.file.allow=never`, `protocol.ext.allow=never` for any server-side fetch except the replication fetch which sets `protocol.ssh.allow=always` (or `protocol.git.allow` if using git-daemon over WG; see T-40).
  - `uploadpack.packObjectsHook` and `uploadpack.allowFilter` are set explicitly (hook unset, filter allowed only for blob:none / tree:0 partial clones if we support them).
  - Environment sanitization: `exec.Cmd.Env` is constructed from an allowlist (`PATH`, `LANG=C`, `GIT_*` we set), never inherited.

#### T-04 Command injection into git subprocess
- Description: User-controlled strings (repo names, ref names, SHAs, paths, usernames) reach `exec` argv or a shell.
- Attack scenario: Repo name `foo;rm -rf /` or ref `--output=/etc/passwd` or path `-c core.pager=...` passed as an argument that git interprets as an option.
- Affected: SSH exec handler, diff/blame/log rendering, hooks.
- Impact: Critical.
- Likelihood: Medium (classic; easy to introduce).
- Mitigations:
  - Never invoke a shell. `exec.Command("git", args...)` with argv built from validated components; no `sh -c`.
  - Repo names must match `^[a-z0-9][a-z0-9-]{0,38}$` and owners `^[a-z0-9][a-z0-9-]{0,38}$`; the repo path on disk is `<root>/<owner>/<repo>.git` built from those validated tokens only.
  - Every user-controlled positional argument to git is preceded by `--` and additionally validated: refs against `check-ref-format`, SHAs against `^[0-9a-f]{40}$` (or 64 for SHA-256 repos), paths against "no leading `-`, no NUL, no `..` segment, no absolute".
  - The SSH exec parser accepts exactly two forms: `git-upload-pack '<path>'` and `git-receive-pack '<path>'` (also with `git ` prefix form), parsed with a strict grammar (single-quoted path, no other characters), not shell-split.
  - Fuzz tests for the exec-line parser and for every argv builder.

#### T-05 SSH shell escape / feature abuse
- Description: Attacker obtains a shell, port forward, agent forward, SFTP, X11, env injection, or PTY via the SSH server.
- Attack scenario: `ssh -t forge.example bash`, `ssh -L`, `ssh -R`, `-o SendEnv=LD_PRELOAD`, subsystem request `sftp`.
- Affected: SSH server.
- Impact: Critical.
- Likelihood: High (automated scanners try this constantly).
- Mitigations:
  - Use `golang.org/x/crypto/ssh` server directly, not sshd. Accept only channel type `session`; reject `direct-tcpip`, `forwarded-tcpip`, `x11`, `auth-agent@openssh.com`, and all global requests (`tcpip-forward`, `cancel-tcpip-forward`, `no-more-sessions@openssh.com` is fine).
  - On the session channel, accept only `exec` requests; reject `shell`, `pty-req`, `subsystem`, `env`, `x11-req`, `auth-agent-req@openssh.com`, `signal` (except during exec, forward SIGINT/SIGTERM only), `window-change`. Reply `false` and close.
  - Authentication: public key only (`PasswordCallback=nil`, `KeyboardInteractiveCallback=nil`), `MaxAuthTries=3`, per-connection auth deadline 10 s, total handshake deadline 15 s.
  - Key algorithms: ed25519, ecdsa-sha2-nistp256/384, rsa-sha2-256/512 (min 3072-bit); reject `ssh-rsa` SHA-1 signatures, ssh-dss. KEX: curve25519-sha256, ecdh; MAC: hmac-sha2-256-etm; ciphers: chacha20-poly1305, aes-gcm. No `none`.
  - Host key: ed25519 only. Publish SSHFP records and the fingerprint on the Gemini home page.
  - The exec handler resolves the account from the *key fingerprint used to authenticate* (from `ssh.Permissions.Extensions` set in the auth callback), never from the username field, which is fixed to `git` and otherwise ignored.
  - The forwarded env for the git subprocess is our allowlist (T-03).
  - Connection limits: max 32 concurrent sessions per source IP, 512 total; idle timeout 60 s with no data; hard cap 1 h per session (configurable for huge clones); rate-limit new connections per IP (token bucket 10/min burst 20) with failed-auth penalty.

#### T-06 Huge repositories / disk exhaustion via push
- Description: A user pushes a very large pack or many packs to fill disk or exhaust inodes.
- Attack scenario: Push 50 GB of random blobs; or thousands of repos each near the quota.
- Affected: receive-pack, disk, replicas (they fetch it), gc.
- Impact: High (denial of service for the node and all repos on it).
- Likelihood: High.
- Mitigations:
  - `receive.maxInputSize=<per-repo or global, default 256 MiB>` set via `-c` for every receive-pack. Larger pushes require an owner-raised quota, logged.
  - Per-repo size quota (default 1 GiB) and per-account total quota (default 5 GiB) enforced in pre-receive: measure `git count-objects -v` (size + size-pack) plus incoming pack size (`$GIT_QUARANTINE_PATH` in pre-receive contains the incoming objects; `du` it) and reject if over quota *before* objects leave quarantine.
  - Per-object blob size limit (default 100 MiB) enforced in pre-receive via `git rev-list --objects --filter=blob:limit=100M --missing=print`-style check or `cat-file --batch-check` on new objects. Prefer this over relying on hooks after unpack.
  - `transfer.unpackLimit=100` (default; keep packs as packs, reduce loose-object inode explosion), `receive.unpackLimit` same.
  - `gc.auto=0` for server-triggered auto-gc; run repacking from a scheduler (T-08) with `git repack -a -d --write-bitmap-index --window=<small> --depth=50 -c pack.windowMemory=256m -c pack.threads=2 -c pack.deltaCacheSize=64m`. `gc.autoPackLimit` irrelevant when auto gc off.
  - `core.bigFileThreshold=8m` to avoid deltifying huge blobs (bounds memory).
  - Filesystem: repos on a dedicated volume with reserved space (5%) for the DB and logs on a separate volume; `MemoryMax`, `TasksMax` on the unit; disk usage metrics with alerts at 80%.
  - Refuse new repos and new pushes when free space < 10%; refuse Titan uploads when < 5% (writes ordered by importance).
  - Replicas apply the same limits on fetch (`-c transfer.fsckObjects=true -c fetch.fsckObjects=true`) and refuse to fetch a repo whose leader-reported size exceeds quota (defence in depth against a compromised leader, T-42).

#### T-07 Decompression / resource attacks (pack bombs, delta chains, ref explosion)
- Description: Small inputs that cause large CPU/memory/disk use: deeply chained deltas, highly compressed packs, millions of refs, huge commit messages, or a pathological history that makes `rev-list`/`merge-base` slow.
- Attack scenario: Push a 1 MiB pack that expands to 20 GiB of objects; or 500k tags; or a commit with a 1 GiB message; or a branch that is 10M commits deep in a straight line to make `--is-ancestor` slow.
- Affected: receive-pack, index-pack, fsck, upload-pack (advertisement), diff rendering, gc.
- Impact: High.
- Likelihood: Medium.
- Mitigations:
  - `receive.maxInputSize` bounds the compressed input; add a pre-receive check on `du` of quarantine (expanded size) and reject if > 4x the input size or over quota.
  - `pack.deltaCacheLimit`, `core.deltaBaseCacheLimit=64m`, `pack.windowMemory=256m` set on every git invocation; `index-pack` inherits.
  - Every git subprocess runs under `prlimit`-style limits: `RLIMIT_AS` 2 GiB (or `RLIMIT_DATA`), `RLIMIT_NPROC`, `RLIMIT_FSIZE` = quota, `RLIMIT_CPU` = 300 s, plus a Go `context.WithTimeout` (receive: 15 min, upload: 1 h, render: 10 s) that kills the process group.
  - Maximum ref count per repo (default 10,000) enforced in pre-receive: `git for-each-ref --count` plus the number of new refs in the update list.
  - Maximum commit message size (64 KiB) and maximum tree entry count per tree (50,000) enforced in the pre-receive object scan.
  - Ref advertisement: enable protocol v2 (`protocol.version=2`) so `upload-pack` does not advertise all refs; set `uploadpack.allowAnySHA1InWant=false`, `uploadpack.allowReachableSHA1InWant=false`, `uploadpack.allowTipSHA1InWant=false` (default), `uploadpack.allowFilter=true` only if partial clone supported.
  - Diff rendering is capped (T-13).

#### T-08 gc / repack contention and repository corruption
- Description: Concurrent gc during push or replication corrupts repos or causes availability loss; a crafted push causes gc to spin.
- Affected: repo store.
- Impact: High.
- Likelihood: Low-Medium.
- Mitigations:
  - Disable auto gc everywhere (`gc.auto=0`). Scheduler runs `git repack`/`gc` per repo, serialised with pushes via an in-process per-repo write lock and `git`'s own `gc.pid`/`packed-refs.lock`; `gc.reflogExpire=90 days`, `gc.pruneExpire=2.weeks.ago` (never `now`; concurrent fetches on replicas need grace).
  - `core.packedRefsTimeout=5000`, `core.filesRefLockTimeout=5000`.
  - Use `git maintenance`-style incremental tasks (`incremental-repack`, `pack-refs`) under low priority (`nice 10`, `ionice -c3`).
  - Backups: nightly `git bundle --all` or filesystem snapshot to object storage (encrypted with age), verified by restoring and `fsck`ing one random repo per night.

#### T-09 Symlink and submodule attacks against the server
- Description: Beyond T-02 (attacks on users), the server itself may follow a symlink when reading a path inside a tree (e.g. rendering `README`) or resolving `.gitmodules`.
- Attack scenario: A tree entry `README -> ../../../../etc/passwd` mode `120000`; server "renders README" by resolving the symlink target on disk.
- Affected: Gemini rendering.
- Impact: Medium (info disclosure) to High.
- Likelihood: Low (only if implementation is sloppy; the bare repo has no worktree).
- Mitigations:
  - Never materialise trees on disk. Read blobs via `git cat-file --batch` / go-git in-memory; if an entry's mode is `120000`, render it as "symlink to <target>" text, never follow.
  - No server-side checkout ever (invariant I-6).
  - Submodules are rendered as links to the recorded URL only if it matches `^(https|gemini|ssh)://`; never fetched.

#### T-10 Unauthorized repository access (read)
- Description: Reading a private repo or private issue without permission, via SSH, Gemini, feeds, or replication side channels.
- Attack scenario: Guess a private repo path; use `git ls-remote`; read the Gemfeed of a private repo; find refs via `uploadpack.allowAnySHA1InWant`; timing differences between "does not exist" and "forbidden".
- Affected: authz core, upload-pack, Gemini handlers, feeds.
- Impact: High (for private data), Medium otherwise.
- Likelihood: High.
- Mitigations:
  - Single authz function `Can(actor, repo, action) (bool, error)`; every handler calls it; a test enumerates all routes and asserts each calls authz (route table with an explicit `authz:` field, and a startup check that no route lacks one).
  - Unauthorized and nonexistent repos return the same Gemini status (`51 Not found`) and the same SSH error text; both go through the same code path with the same DB queries to avoid timing leaks.
  - Feeds for private repos require a client cert with access; feeds never include private repos in global/aggregate feeds.
  - Replication only ships repos to replicas that are allowed to serve them (private repos may be configured leader-only until per-node ACL exists).
  - `uploadpack.allowAnySHA1InWant=false`, `uploadpack.hideRefs=refs/forge/`.

#### T-11 Compromised SSH key
- Description: A user's private key is stolen or the user is a departed collaborator.
- Attack scenario: Attacker pushes malicious commits to a repo the user had access to.
- Affected: SSH auth, push path.
- Impact: High (scoped to that user's access).
- Likelihood: Medium.
- Mitigations:
  - Users can register multiple keys and revoke any key immediately; revocation checked per connection (key lookup by fingerprint on every auth, no caching beyond a few seconds).
  - Every push records `(account, key fingerprint, source IP, ref, old, new)` in the audit log and emits an event that owners can see in the repo's Gemfeed ("pushed by ... with key ...").
  - Optional per-repo requirement for signed commits/tags (`gpg`/`ssh` signatures verified against keys registered to the *account*, i.e. `gpg.ssh.allowedSignersFile` generated from the DB) so a stolen SSH key alone cannot produce accepted commits.
  - Key age visible; optional expiry (default none, configurable per instance).
  - Owners can force-restore a ref from the reflog (server-side `core.logAllRefUpdates=true` on bare repos, `gc.reflogExpire=90 days`).

### 4.2 Gemini and TLS identity

#### T-12 Compromised or stolen Gemini client certificate
- Description: Client cert + key extracted from a user's browser directory.
- Attack scenario: Attacker uses the cert to post as the user, edit their issues, change repo settings, add their own SSH key.
- Affected: Gemini/Titan identity.
- Impact: High.
- Likelihood: Medium (client certs are stored unencrypted by many Gemini clients).
- Mitigations:
  - Multiple certs per account, each individually revocable; revocation list checked on *every* request (DB lookup on fingerprint; the DB row has `revoked_at`).
  - Identity is the SHA-256 of the DER-encoded certificate, not the subject, not the public key alone (a public-key-only pin would allow re-signing with different validity to bypass expiry checks; a DER pin is simplest and unambiguous). Store both DER hash and SPKI hash to support future rotation-by-same-key UX.
  - Cert expiry policy: honour `NotAfter`; refuse certs with `NotAfter` > 5 years out at registration (warn) and reject expired certs at request time with status `62 Certificate not valid`. Refuse `NotBefore` in the future.
  - Sensitive actions (add SSH key, add cert, change email/recovery, delete repo, transfer ownership) require a *fresh* confirmation via Titan with a one-time token bound to the cert (T-19) and produce a notification event visible on the account page and in the account's Gemfeed.
  - Security events page shows every request that changed account state with cert fingerprint, time, IP.

#### T-13 Malicious diffs / rendering attacks
- Description: Diff or blob rendering of huge, binary, or crafted content exhausts CPU/memory or injects into Gemini output.
- Attack scenario: A 200 MiB single-line minified JS file; a diff touching 50k files; a file with a million tiny hunks; a file containing `\r\n20 text/gemini\r\n` and `=> ` lines.
- Affected: Gemini rendering.
- Impact: Medium (DoS) / Medium (injection, see T-15/T-16).
- Likelihood: High.
- Mitigations:
  - Diff limits: max 1,000 files, max 10,000 lines total, max 1 MiB rendered; beyond that show "diff too large" with a link to fetch the patch raw (`git diff-tree -p` streamed with the same cap) or clone.
  - Binary detection: if blob contains NUL in first 8 KiB or `git diff --numstat` reports `-`, render "binary file, N bytes" only.
  - Blob view: max 1 MiB rendered, max 500 chars per line displayed (truncate with marker), rest as "raw" download with `text/plain; charset=utf-8` or `application/octet-stream`.
  - All rendering runs via `git diff-tree --no-color --no-ext-diff --no-textconv -M -p` with `-c diff.renameLimit=1000 -c diff.algorithm=myers` (not patience/histogram on untrusted input; myers is bounded) and a 10 s timeout.
  - Every rendered line passes through the gemtext escaper (T-16).

#### T-14 README / markdown rendering fetching remote content
- Description: Conversion of markdown (or other markup) to gemtext follows remote links, images, or includes.
- Attack scenario: README references `![](http://attacker/x)` or a `{% include %}`; the renderer fetches it (SSRF into the WireGuard mesh, metrics port, or Vultr metadata `169.254.169.254`).
- Affected: Gemini rendering.
- Impact: High (SSRF to internal services).
- Likelihood: Medium.
- Mitigations:
  - Renderers are pure functions of the blob bytes; the daemon has no HTTP client in the rendering package (enforced by an import test / `depguard`).
  - Gemtext READMEs are served with the escaper (T-16). Markdown-to-gemtext uses a minimal, dependency-free converter that turns images into `=> url` link lines (after URL validation) and never resolves anything.
  - Network egress from the daemon restricted by nftables to: WireGuard peers, DNS (if needed), and nothing else; `IPAddressDeny=any` + `IPAddressAllow=<wg subnet>` in the systemd unit. Metadata service `169.254.169.254` explicitly denied.

#### T-15 Gemini header / status injection via CRLF
- Description: User-controlled text ends up in a Gemini response header line (`<status><SP><META>\r\n`), e.g. in a redirect target, an input prompt, a MIME parameter, or an error message.
- Attack scenario: Repo description `foo\r\n20 text/gemini\r\n=> gemini://evil/ click` used in a `10` input prompt META; client parses the injected header.
- Affected: Gemini handler.
- Impact: Medium (phishing, cache confusion).
- Likelihood: Medium.
- Mitigations:
  - A single `writeHeader(status int, meta string)` function that rejects/strips any byte < 0x20 or 0x7F from META, enforces META <= 1024 bytes, and is the only way to write a header (lint rule / grep test).
  - Redirect targets (`30`/`31`) are only ever built from validated internal paths, never from user input.
  - Input prompts (`10`/`11`) use fixed strings.

#### T-16 Gemtext injection (user content creating links, headings, preformat toggles)
- Description: In gemtext, line semantics are decided by the first characters (`=>`, `#`, `*`, `>`, `` ``` ``). User content embedded in a page can create links to phishing sites, break out of preformatted blocks, or forge UI elements ("=> gemini://evil/ Approve this review").
- Attack scenario: Issue comment body contains `=> gemini://evil.example/login Sign in to continue`.
- Affected: Every page containing user content.
- Impact: Medium.
- Likelihood: High.
- Mitigations:
  - Decision: user content (issues, comments, review text, descriptions) is rendered *inside a preformatted block* by default? No: that destroys readability and links are useful. Decision: allow gemtext from users but *escape in-context* and *mark* links:
    - Lines beginning with `` ``` `` inside user content are escaped by prefixing a space (prevents toggling our preformat state; user content cannot open a preformatted block; a future "code block" feature can whitelist fenced blocks with the server managing the toggles).
    - Link lines are allowed but rewritten: the server parses `=> URL label`, validates the URL scheme against `gemini|https|http|mailto` (reject `file`, `javascript`, `data`, and anything else), rejects URLs pointing at our own write endpoints (`titan://`) and internal hosts, and re-emits as `=> URL [user link] label` so links from user content are visibly marked and cannot be confused with UI links. UI action links live in a fixed footer section with a fixed prefix (e.g. `=> /... ▸ Action`) that user content cannot produce because `▸` is stripped from user-supplied labels.
    - Headings from user content are demoted (`#` -> `##` etc.) or prefixed with a space, so page structure cannot be forged. Quote and list lines are allowed as-is.
    - When user content is rendered inside our own preformatted block (e.g. code in a diff), lines beginning with `` ``` `` get a leading space (the only escape needed there).
  - The escaper is one function, unit tested with a corpus, used by every template.
  - Non-user text (repo names, usernames) is restricted by regex and needs no escaping.

#### T-17 Unicode / RTL / homoglyph trickery in filenames, refs, and names
- Description: Bidi overrides (U+202E), zero-width characters, and confusables in filenames, branch names, or display names make a malicious file look benign (`README.md` displayed for `exe.dm‮README`), or impersonate another user.
- Affected: Rendering, identity display.
- Impact: Medium.
- Likelihood: Medium.
- Mitigations:
  - Account names, repo names: ASCII-only regex (T-04).
  - Ref names: reject non-ASCII and control chars server-side in pre-receive.
  - Filenames: cannot be restricted (repos are user data), but the renderer escapes: any code point in Cc, Cf (incl. bidi controls, ZWJ/ZWSP), Zl, Zp, or unassigned is rendered as `<U+XXXX>`; filenames containing them get a `[!]` marker. NFC-normalise for display and comparison.
  - Display names (free-form) are stripped of Cf/Cc and limited to 64 code points; usernames are always shown next to display names.
  - Reject `.gitmodules`/`.gitattributes` names with confusables in the pre-receive scan (T-02) by NFKC-folding entry names and comparing.

#### T-18 User enumeration and timing attacks on cert/key lookup
- Description: Distinguish "cert not registered" from "registered but wrong" or enumerate usernames via response differences; measure lookup timing to learn about DB contents.
- Affected: Gemini identity, SSH auth, account pages.
- Impact: Low.
- Likelihood: Medium.
- Mitigations:
  - Profile pages are public by design (usernames are public); enumeration of usernames is accepted. What must not be enumerable: private repos (T-10), email/recovery addresses, and cert/key fingerprints of other users.
  - Cert lookup: indexed lookup by fingerprint; the code path for unknown vs known cert executes the same work (`SELECT ... WHERE fp = ?` then constant-time compare of nothing); status for unregistered cert on a page requiring auth is `60` in all cases.
  - SSH: `PublicKeyCallback` does the DB lookup and returns the same error for unknown key vs known key on wrong account; `MaxAuthTries=3`; never reveal which of a client's offered keys "almost" worked.
  - Registration endpoints are rate-limited per IP and per cert.

#### T-19 CSRF-equivalent: link-click triggers a write
- Description: Gemini has no cookies, but a client with a cert selected for the site will present it automatically. If any *Gemini* (GET-like) request can perform a write, a link on an attacker's capsule can make the victim's client perform it.
- Attack scenario: Attacker posts `=> gemini://forge/repo/x/settings/delete?confirm=1` on their capsule; victim with cert active clicks it.
- Affected: All state-changing endpoints.
- Impact: High.
- Likelihood: High (if any write is exposed over Gemini).
- Mitigations:
  - Invariant: no Gemini request mutates state. All writes go through Titan (or SSH push). Gemini `10`/`11` input prompts are only used for *search/navigation*, never for writes (a `10` response that then writes on the follow-up would be a write via GET).
  - Titan writes additionally carry a per-user, per-purpose, short-lived (10 min) token in the `token=` parameter, issued on the Gemini page that offers the action (e.g. the "delete repository" page shows the Titan URL with a fresh token). Tokens are 128-bit random, stored hashed in the DB with (user, purpose, repo, expires), single-use for destructive actions. This defends against clients that might auto-upload, and against token guessing (T-21).
  - Titan requests without a client cert are rejected with `60` before reading the body.

#### T-20 Denial of service against the Gemini/Titan listener (slowloris, connection floods, TLS cost)
- Description: Many connections held open, slow request lines, TLS handshake CPU exhaustion, renegotiation.
- Affected: 1965 listener.
- Impact: Medium-High.
- Likelihood: High.
- Mitigations:
  - Per-connection deadlines: TLS handshake 10 s; request line must arrive within 10 s; request line max 1024 bytes (Gemini spec) — Titan request line max 2048; response write deadline scaled to size with minimum throughput 10 KiB/s; absolute cap 5 min for Gemini, 30 min for Titan uploads.
  - Concurrency limits: 64 connections per source IP (/32 v4, /64 v6), 4096 total per node; over-limit connections are accepted and immediately closed with `44 Slow down` (Gemini) so clients back off; a small pool for cert-holding users is reserved.
  - TLS config: TLS 1.2 minimum (Gemini clients), TLS 1.3 preferred; no renegotiation (Go default); session tickets on with rotating keys (in-memory, per POP; fine because anycast connections are sticky to a POP); no client cert *verification* (we only pin fingerprints), so no CA chain work; `ClientAuth=RequestClientCert`.
  - systemd unit: `CPUQuota=<N*80>%`, `MemoryMax=`, `TasksMax=2048`, `LimitNOFILE=65536`.
  - Kernel: SYN cookies on, `net.ipv4.tcp_max_syn_backlog`, `nf_conntrack` sized or disabled for the listener ports; anycast spreads floods across POPs; upstream (Vultr) DDoS protection is the last resort for volumetric attacks.
  - Cheap paths first: rate limiting and quota checks happen before any DB or git work.

### 4.3 Titan (uploads)

#### T-21 Spoofed / replayed Titan token; replay of uploads
- Description: Attacker guesses, steals, or replays a Titan `token=` to perform a write as another user; or replays an intercepted upload.
- Affected: Titan.
- Impact: High.
- Likelihood: Low with proper tokens.
- Mitigations:
  - Tokens (T-19): 128-bit CSPRNG, stored as SHA-256, bound to (cert fingerprint, purpose, target), expire in 10 min, single-use for destructive/idempotency-sensitive actions, consumed atomically in the same DB transaction as the write.
  - Identity is always the client cert; a token alone never authorises anything (both required).
  - TLS prevents interception; replay within a session is prevented by single-use tokens. For non-token uploads (if any are allowed for low-risk actions like comments) the body hash + user + 1 min window is used as an idempotency key to collapse accidental duplicates.
  - Tokens never appear in logs; request lines are logged with `token=` redacted.

#### T-22 Titan abuse: oversized uploads, size/MIME lies, disk exhaustion
- Description: `size=` mismatch with body, huge `size=`, streaming forever, MIME spoofing (`text/gemini` for a binary, `application/x-executable` for release assets to trick downloaders).
- Affected: Titan, disk.
- Impact: High.
- Likelihood: High.
- Mitigations:
  - Parse `size=N` strictly (`^[0-9]{1,12}$`), reject if N > limit for the endpoint *before* reading any body: issue/comment/review text 64 KiB, metadata 4 KiB, release asset 256 MiB (owner-adjustable up to node limit), avatar 64 KiB.
  - Read exactly N bytes with `io.LimitReader(conn, N)` and a per-upload deadline; if the client sends fewer, the upload is discarded (`40 Temporary failure`); if it sends more, the connection is closed after N.
  - Quota check (per-account release bytes, per-repo) *before* reading the body, and again atomically at commit (disk could fill mid-upload).
  - MIME allowlist per endpoint: text endpoints accept only `text/gemini` and `text/plain` (with optional `;charset=utf-8`, no other parameters) and the body must be valid UTF-8 with no NUL; release assets accept a fixed list (`application/octet-stream`, `application/gzip`, `application/zip`, `application/x-xz`, `application/x-tar`, `application/pgp-signature`, `text/plain`) and the stored MIME is *our* classification (sniff first 512 bytes; if sniff disagrees with declared type for text/* vs binary, store as `application/octet-stream`). Served release assets are always `application/octet-stream` unless they are `text/plain` and pass UTF-8 validation; never `text/gemini` (prevents an asset being rendered as a page with injected links).
  - Writes are atomic: open with `O_TMPFILE|O_WRONLY` in the destination directory (fallback: `os.CreateTemp` in the same directory), `fsync`, `linkat`/`rename` into place, `fsync` the directory. Never write directly to the final path.
  - Release assets stored under `<root>/assets/<owner>/<repo>/<release-id>/<sha256>` named by content hash; the human filename lives in the DB and is validated (`^[A-Za-z0-9._-]{1,128}$`, no leading `.`).
  - Total per-node asset directory has a hard cap; uploads refused at 95%.

#### T-23 Path traversal via Titan paths, repo names, asset names
- Description: `titan://host/repo/../../etc/cron.d/x`, encoded traversal (`%2e%2e`), NUL bytes, symlinks placed via some other path.
- Affected: Titan, Gemini path routing, asset store.
- Impact: Critical.
- Likelihood: High (constant automated probing).
- Mitigations:
  - URL parsing: decode percent-encoding once, reject any path containing NUL, `\`, or a `..`/`.` segment, or non-printable characters; reject if re-encoding does not round-trip; then match against a fixed route table with typed parameters (owner, repo, id) validated by regex. Filesystem paths are constructed *only* from those validated parameters, never from the raw path.
  - `filepath.Clean` + `strings.HasPrefix(root+"/")` check as a second layer, with `filepath.EvalSymlinks` on the parent and a refusal if the resolved path escapes root.
  - The daemon opens files with `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)` on Linux (via `golang.org/x/sys/unix`) for any path under a user-influenced directory; `O_NOFOLLOW` on final component elsewhere.
  - The daemon never creates symlinks; the scrub job alerts if any symlink exists under the data root.
  - Fuzz tests on the router with traversal corpora.

#### T-24 MIME confusion on serving
- Description: Serving user bytes with a type that makes a Gemini client interpret them dangerously (e.g. as `text/gemini` with links, or a `image/svg+xml` in clients that render it with a browser engine).
- Affected: Gemini raw/blob/asset serving.
- Impact: Medium.
- Likelihood: Medium.
- Mitigations:
  - Raw blob serving uses an allowlist of served types derived from content sniffing and extension: `text/plain; charset=utf-8` for UTF-8 text, `application/octet-stream` otherwise, plus `image/png|jpeg|gif` only when the magic bytes match. Never serve `text/gemini` for raw blobs (rendered gemtext files go through the escaper and are marked as user content). Never `image/svg+xml`, never `text/html`.
  - The `META` for raw responses is fully server-generated (T-15).

### 4.4 Database

#### T-25 SQL injection
- Description: User strings concatenated into SQL.
- Affected: all DB code.
- Impact: Critical.
- Likelihood: Low with discipline, High without.
- Mitigations:
  - All queries are parameterised (`?` placeholders) via `database/sql` or `sqlc`-generated code; dynamic SQL (ORDER BY columns, table names) uses allowlisted constants. `go vet`-style linter (`sqlclosecheck`, `rowserrcheck`) plus a grep test that no `fmt.Sprintf` result is passed to `Query`/`Exec`.
  - FTS queries (issue search) go through a query builder that tokenises user input and quotes each token, since FTS5 has its own syntax that accepts operators.
  - The DB connection is opened with `?_pragma=foreign_keys(1)&_pragma=trusted_schema(0)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)`; `trusted_schema=0` prevents schema-embedded functions; `SQLITE_DBCONFIG_DEFENSIVE` enabled; `load_extension` disabled (default in most Go drivers; verify).

#### T-26 WAL corruption, crash consistency, disk-full behaviour
- Description: Power loss, disk full, or two processes opening the DB corrupt the WAL or lose commits; the daemon keeps serving with a broken DB.
- Affected: SQLite.
- Impact: High.
- Likelihood: Medium.
- Mitigations:
  - Exactly one process opens the DB (the daemon); the admin CLI talks to the daemon over a unix socket rather than opening the DB (except in explicit offline maintenance mode with the daemon stopped, enforced by an flock on the DB path).
  - WAL mode with `synchronous=NORMAL` (FULL for the leader-critical tables if needed) on a filesystem with barriers; DB on a separate volume from repos with 512 MiB reserved; WAL checkpointing (`PRAGMA wal_checkpoint(TRUNCATE)`) scheduled; `journal_size_limit=64MiB`.
  - On startup: `PRAGMA integrity_check` (quick_check if DB > 1 GiB); refuse to start if it fails and alert.
  - Disk-full handling: writes fail closed (`SQLITE_FULL` surfaces as `40`/`41` to Titan, push rejected in pre-receive when the event write fails); reads continue. Free-space watermarks (T-06).
  - Backups: `sqlite3 .backup`-equivalent via the online backup API every hour to an age-encrypted object store; restore tested weekly in CI/staging. Litestream-style continuous WAL shipping is an option later.
  - DB file mode 0600, owned by the daemon user; never on NFS.

#### T-27 Metadata tampering by a compromised node
- Description: A replica with a copy of the DB (or a read-only replica DB) modifies ACLs or identity rows and they propagate.
- Affected: metadata replication.
- Impact: Critical.
- Likelihood: Low.
- Mitigations: see T-41/T-42. Global metadata (accounts, keys, certs, ACLs) has a single global leader; replicas hold read-only copies received from the leader over WireGuard and *never* accept metadata writes locally. Forwarded writes are validated by the leader from scratch.

### 4.5 Identity lifecycle

#### T-28 Account recovery attacks
- Description: Attacker abuses recovery to take over an account (social engineering an admin, weak recovery challenge, recovery via a stolen secondary factor).
- Affected: recovery flow, admin CLI.
- Impact: High.
- Likelihood: Medium.
- Mitigations:
  - Primary recovery is self-service and cryptographic: an account with >=1 registered SSH key or >=1 other cert can add a new cert by signing a server-issued challenge (`ssh-keygen -Y sign` with namespace `forge-recovery@<host>`, challenge includes the account, new cert fingerprint, nonce, and expiry of 10 min; verified with `ssh-keygen -Y verify`-equivalent in Go). Recovery events notify the account (Gemfeed + optional email if configured).
  - Encourage users at signup to register at least two credentials (a second cert or an SSH key); show a warning banner until they do.
  - Admin recovery (no remaining credentials) is a documented manual process: admin verifies ownership via an out-of-band proof (e.g. a signed commit in the user's repo history with a key that matches, or a signed message with a key that was previously registered), records the evidence in the audit log, and the account enters a 7-day "recovery pending" state visible on its profile before the new credential activates. There is no email-only recovery.
  - Recovery endpoints are rate limited (5/hour/account, 20/hour/IP).

#### T-29 Registration abuse / Sybil accounts
- Description: Unlimited free self-signed certs allow unlimited accounts for spam and quota evasion.
- Affected: registration, quotas.
- Impact: Medium.
- Likelihood: High.
- Mitigations:
  - Registration rate limit per IP (/32, /64) and per POP; global per-hour cap with alerting; new accounts start with reduced quotas (1 repo, 100 MiB, 20 issues/day) that grow with age or admin approval; instances may run invite-only (admin-issued invite tokens) — default for the reference deployment.
  - Optional proof-of-work or admin-approval queue as pluggable gates.

### 4.6 Multi-POP, replication, network

#### T-30 BGP prefix hijack by a third party
- Description: Another AS announces our /24 or /48 (or a more specific) and attracts our traffic.
- Affected: Availability, and confidentiality/integrity if clients do not verify (TOFU first contact).
- Impact: High.
- Likelihood: Low-Medium (mis-originations are common; targeted hijacks rarer).
- Mitigations:
  - RPKI ROAs published for both prefixes with `maxLength` equal to the prefix length (/24, /48) so more-specifics are RPKI-invalid; origin AS215520 only. Monitor ROA validity.
  - IRR `route`/`route6` objects and `aut-num` with policy in RIPE DB; ASN in PeeringDB.
  - Monitoring: BGPalerter (or RIPE RIS live) alerts on unexpected origin, more-specific, or path changes; RPKI-invalid announcements alerts.
  - Client-side, TOFU pinning of the TLS server cert and SSH host key means a hijacker cannot impersonate us to *returning* clients; publish fingerprints out-of-band (repo README on other forges, DNS `SSHFP`, DNSSEC-signed TLSA-style TXT record in Cloudflare with DNSSEC enabled) to protect first contact.
  - We cannot prevent hijacks; we can detect and escalate to upstreams.

#### T-31 Route leaks and our own mis-announcements
- Description: Our BIRD config leaks a full table, announces someone else's prefix, or announces bogons; upstream depeers us.
- Affected: Availability, reputation.
- Impact: High.
- Likelihood: Low with filters.
- Mitigations:
  - BIRD export filter: `if net ~ [ 203.0.113.0/24 ] then accept; reject;` (and the /48 for v6) — an explicit prefix list of exactly our prefixes, nothing else, with `bgp_community.add((65535, 65281))` (NO_EXPORT) only if we ever want a POP to drain.
  - BIRD import filter from upstream: accept only default route (or nothing; we can static-route default) — never import a full table into the kernel; `import limit 10 action block`; reject bogons and our own prefixes on import.
  - Session security: TCP MD5 (`password`) or TCP-AO where supported; `multihop` off; `hold time 90`; graceful restart enabled for maintenance.
  - Config is generated by OpenTofu/templates, reviewed, and validated with `bird -p` in CI before deploy.
  - Prefix monitoring (T-30) also alerts on unexpected *more* announcements from AS215520.

#### T-32 Anycast-specific: TCP session instability, POP flapping, inconsistent state across POPs
- Description: Route changes mid-connection break TCP (esp. long git clones and Titan uploads); a POP that is up at layer 3 but broken at layer 7 blackholes its catchment.
- Affected: Availability.
- Impact: Medium.
- Likelihood: Medium.
- Mitigations:
  - Health-checked announcement: a local watchdog checks the daemon (Gemini `20` on `/healthz`, SSH banner, WireGuard peer reachability, DB integrity flag) and withdraws the prefix (stops BIRD export) on failure; announcement is only made after the daemon is healthy. Withdraw before planned restarts.
  - Long transfers: prefer resumability (git supports retry; Titan uploads are all-or-nothing and capped at 256 MiB); document that users should retry.
  - Read consistency: replicas may lag; pages show "last synced" and the leader is used for read-after-write for the writing user (forward the read, or redirect the user's session to leader for 30 s via a state flag in the token).

#### T-33 WireGuard mesh compromise / misconfiguration
- Description: A stolen node key joins the mesh; over-broad `AllowedIPs` lets one node spoof another's address; unencrypted replication path.
- Affected: Inter-node.
- Impact: High.
- Likelihood: Low.
- Mitigations:
  - One WireGuard keypair per node, generated on the node, never leaves it (public key registered in the SOPS-encrypted peer list); `AllowedIPs` for each peer is exactly its /32 and /128 mesh address; `PersistentKeepalive` on; preshared keys per pair (optional, quantum hedge).
  - Rotation: node keys rotated on every rebuild (nodes are immutable/rebuilt by OpenTofu) and at least every 180 days; a compromised node is removed from all peers' configs within minutes via the same pipeline.
  - All replication, forwarded writes, and metrics scraping bind *only* to the wg0 address; nftables drops these ports on public interfaces.
  - WireGuard is transport-level; application-level node identity is still checked (T-41).

#### T-34 Replication compromise: replica serves tampered data
- Description: A compromised replica alters objects or refs it serves to readers (git clone over SSH from that POP, Gemini views).
- Affected: Read path integrity for users landing on that POP.
- Impact: High (but bounded: cannot alter leader).
- Likelihood: Low.
- Mitigations:
  - Signed ref state: the leader publishes, per repo, a signed manifest (ed25519 with the leader node key; the set of leader public keys is distributed via SOPS) of `(ref, sha)` pairs plus the DB "repo head state"; replicas verify on fetch and refuse to serve if verification fails; a scrub on the replica re-verifies periodically. Users can also verify via signed tags/commits from authors (we surface signature status in the UI).
  - Replication fetch runs with `fetch.fsckObjects=true`, `transfer.fsckObjects=true`, and `--prune`-less mirror fetch (`+refs/*:refs/*` from the leader only; refspec restricted to `refs/heads/*`, `refs/tags/*`, `refs/forge/*`).
  - Periodic cross-check: a monitoring job clones a random repo from each POP (by connecting to each POP's unicast address) and compares ref hashes with the leader; drift alerts.
  - Blast radius: a replica cannot push to a leader (T-41) and cannot write global metadata (T-27).

#### T-35 TLS server key compromise (anycast = same key on every POP)
- Description: Every POP must present the same server certificate for TOFU pinning to work, so the private key exists on every node; compromising any node (or a provider snapshot) yields the key for all POPs. With TOFU there is no CA revocation.
- Affected: Gemini/Titan integrity and confidentiality for all users, on any path where an attacker can also get traffic (BGP hijack, provider MITM, rogue POP).
- Impact: Critical.
- Likelihood: Low-Medium (it is the most valuable secret on the least-trusted machines).
- Options considered:
  1. Same key on every POP (required for TOFU consistency). Chosen.
  2. Per-POP certificates: breaks TOFU as clients move between POPs (anycast means the POP is chosen by routing; a user travelling sees a "certificate changed" warning). Rejected.
  3. HSM / remote signing: not practical for Gemini's plain TLS server key at our scale and would add a cross-POP dependency to every handshake. Rejected for now; revisit if a cheap remote-KMS TLS signer becomes practical.
  4. Delegated per-POP keys signed by a long-lived self-CA: Gemini clients pin the leaf, not a chain, so this does not help TOFU. Rejected.
- Mitigations (for option 1):
  - Key material: ed25519 (or P-256 for older client compatibility — measure; Ed25519 support in Gemini clients is not universal; default P-256) generated on an operator machine, encrypted with SOPS/age, decrypted into memory (`/run/forge`, tmpfs, mode 0400, `RuntimeDirectory=`) at service start, never written to persistent disk on the node. `MemoryDenyWriteExecute`, `ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges` in the unit; core dumps disabled (`LimitCORE=0`, `ProtectKernelTunables`).
  - Provider snapshots: refuse to use Vultr snapshot features on running nodes; disk encryption at rest is not meaningful against the provider but do it anyway (LUKS with key from SOPS at boot) so decommissioned disks are safe.
  - Lifetime: certificate validity 2 years; key rotation every 2 years (or immediately on suspected compromise) with a *pre-announced overlap*: publish the new fingerprint on the home page, in a `/.well-known/forge/tls-rotation.gmi` page (signed with the old key via a gemtext line `# next-cert-sha256: ...` and with an SSH-key signature), in DNS TXT (DNSSEC), and in the project repo 30 days before switching. Because TOFU clients will warn at switch, the warning is expected and documented; the number of clients that never see the notice is minimised by the long overlap.
  - Also: the SSH host key is a second, independent identity that survives a TLS key rotation; users can verify the new TLS fingerprint by cloning `forge-meta` over SSH.
  - Detection: monitoring clients (one per region, non-anycast vantage points) connect and pin; any fingerprint mismatch alerts (catches both key misuse and hijack-with-different-key).

#### T-36 SSH host key compromise
- Same analysis as T-35; same key on every POP. Mitigations: ed25519 host key, SOPS/age custody, tmpfs at runtime, SSHFP records with DNSSEC, rotation with overlap using OpenSSH `HostKey` multiple keys (we can present two host keys during overlap since OpenSSH clients accept any known key: add new key, run both for 90 days, remove old). The `UpdateHostKeys` OpenSSH extension (`hostkeys-00@openssh.com`) can be implemented to push the new key to clients that support it.

#### T-37 Compromised POP (full node compromise)
- Description: Attacker gets root on one POP via a daemon bug, provider, or stolen operator access.
- Affected: Everything on that node.
- Impact: Critical for that node's catchment; must be bounded for the system.
- Likelihood: Low-Medium.
- Blast radius requirements (invariants I-14..I-17):
  - The node's WireGuard key is only its own; other nodes only accept its mesh address for its role.
  - A replica node cannot: push to a leader's repos; write global metadata; sign a leader manifest; decrypt secrets it was not given (per-node SOPS recipients: each node's age key decrypts only its own secrets file containing shared TLS/SSH keys — this is the unavoidable shared blast radius — and its WireGuard key; it cannot decrypt the Vultr/Cloudflare/RIPE tokens, which never go to nodes).
  - Forwarded writes from a replica to a leader carry: the replica's node identity (mutually authenticated over WG, plus an application-level ed25519 signature by the node key), and a *user attestation* the leader can verify independently: the user's full client certificate DER (so the leader recomputes the fingerprint and checks registration/revocation itself) plus the Titan token, which the leader validates against the global token table. The leader re-runs authz from scratch. A compromised replica can thus only replay what a real user actually sent it (bounded by token single-use and expiry), and only for users who were routed to it.
  - For git pushes forwarded from a replica: the replica does not accept receive-pack at all; instead it *proxies* the SSH exec stream to the leader over WG after authenticating the user, and the leader re-authenticates via a signed attestation of the user's SSH key fingerprint plus the raw pack, and re-runs pre-receive. Simpler alternative (M-6 decision): replicas reject `git-receive-pack` with a message directing clients to a leader-specific unicast hostname (`push.<pop>.forge...`) — no forwarding of pushes at all in v1. Chosen for v1: reject + redirect message; proxying is a later milestone.
  - Compromise response runbook: withdraw prefix from that POP (upstream portal / kill BIRD), revoke its WG peer entry and its age recipient across all nodes, rotate the shared TLS + SSH keys (T-35 procedure, accelerated), rebuild the node from OpenTofu, audit recent forwarded writes from that node ID (they are tagged in the audit log).

#### T-38 Leader compromise / leader impersonation
- Description: A node claims leadership for a repo it does not lead; or the actual leader is compromised.
- Affected: Write authority.
- Impact: Critical (scoped to led repos).
- Likelihood: Low.
- Mitigations:
  - Leadership assignments live in the global metadata leader's DB and are distributed as a signed document; replicas fetch a repo only from its assigned leader's mesh address and verify the manifest signature against that specific node's key.
  - Leader nodes are the most hardened tier; fewer of them (initially one global metadata leader, which also leads all repos; scale out later).
  - A compromised leader can rewrite history for its repos; detection relies on signed commits/tags by authors, the audit log shipped off-node in near-real-time (T-46), and users' own clones. Recovery: restore from off-node backups and reflogs.

### 4.7 Secrets, supply chain, infrastructure

#### T-39 Secrets theft (age keys, SOPS files, API tokens)
- Affected: A9, A10, A11.
- Impact: Critical.
- Likelihood: Low-Medium.
- Mitigations:
  - Age key custody: operator keys on hardware tokens where possible (`age-plugin-yubikey`) or in a passphrase-protected file on a full-disk-encrypted laptop; never in the repo, never in CI env without a dedicated CI age key with minimal recipients.
  - `.sops.yaml` `creation_rules` by path: `secrets/nodes/<node>.yaml` -> recipients: {operators, that node's age key}; `secrets/shared/tls.yaml` -> {operators, all node keys}; `secrets/cloud/*.yaml` (Vultr, Cloudflare, RIPE) -> {operators only}; CI has its own key with access only to what CI needs. Rule violations are caught by a CI check that decrypts each file's recipient list and compares against policy.
  - Nodes get a per-node age key generated at first boot (stored in `/etc/forge/age.key`, 0400, root), public key collected into the SOPS config through the OpenTofu pipeline.
  - Cloud tokens: least privilege (Vultr: instance + BGP scopes only; Cloudflare: DNS edit on one zone), rotated quarterly, never on nodes. OpenTofu state stored encrypted (remote backend with SSE, or local state under SOPS); state contains secrets, treat as such.
  - Secrets never logged; a test asserts the logger redacts known secret shapes (`AGE-SECRET-KEY-`, `PRIVATE KEY`, `token=`).

#### T-40 Dependency and toolchain compromise
- Affected: A15.
- Impact: Critical.
- Likelihood: Medium (ecosystem-wide risk).
- Mitigations:
  - `go.mod` + `go.sum` committed; `GOFLAGS=-mod=mod` forbidden in CI (`-mod=readonly`); `GONOSUMDB` empty; `GOPROXY=proxy.golang.org,direct` with `GOSUMDB=sum.golang.org` checks.
  - Minimal dependency policy: stdlib + `golang.org/x/crypto` (ssh) + one SQLite driver (`modernc.org/sqlite`, pure Go, preferred over cgo) + `filippo.io/age` (if used in-process) + a vetted SOPS client. Every new dependency needs a review note in the PR.
  - `govulncheck` in CI and nightly; toolchain pinned via `toolchain go1.xx.y` in `go.mod` and `GOTOOLCHAIN=local` in CI to stop auto-download.
  - Reproducible builds: `-trimpath`, `-buildvcs=false`, `CGO_ENABLED=0`, fixed `GOFLAGS`; CI verifies the hash from two independent builders matches.
  - Release signing: cosign keyless (Sigstore) or a project SSH/minisign key; checksums published; systemd unit only runs a binary whose hash matches the SOPS-distributed expected hash (optional).
  - The `git` binary on nodes: distro package pinned to a version with all known server-side fixes (>= 2.45.x for CVE-2024-32002/32004/32465 family); `unattended-upgrades` for security updates; version checked at daemon start and logged in metrics.
  - OS images: Vultr official images, then hardened by cloud-init from the repo; CIS-ish baseline (no password auth, sshd on a separate management port bound to the WG address only, or via provider console).

#### T-41 Forged inter-node requests (forwarded Titan writes, replication control)
- Covered in T-37: mutual WG + application-level node signature + user attestation (full client cert DER + token) + leader-side authz. Additional: every inter-node message has a nonce and timestamp (5 min window) and is bound to the leader's identity to prevent cross-leader replay; the leader keeps a short-lived nonce cache.

#### T-42 Rogue or lagging leader causing replicas to serve bad data
- Covered in T-34/T-38: replicas verify manifests; if the leader is unreachable, replicas keep serving last-good data with a staleness banner after 15 min; they never fail over to another node's data without an operator-signed leadership change.

#### T-43 Infrastructure-as-code and DNS compromise
- Description: Cloudflare account takeover changes DNS to an attacker IP; OpenTofu pipeline compromise deploys malicious config.
- Impact: High (DNS to attacker defeats first-contact TOFU; returning clients see a cert mismatch).
- Likelihood: Low.
- Mitigations: Cloudflare MFA (hardware keys), API token scoped to one zone, DNSSEC enabled, CAA irrelevant (self-signed), zone change alerts; OpenTofu applies only from a reviewed main branch with a second operator's approval for changes under `infra/`; `tofu plan` output reviewed; state locked.

#### T-44 Prometheus metrics exposure
- Description: Metrics leak usernames, repo names, or internal addresses; the endpoint is reachable publicly.
- Impact: Low-Medium.
- Likelihood: Medium (misbinding is easy).
- Mitigations: bind `/metrics` to the WG address only; nftables denies port on public interfaces; a startup self-check refuses to start if the metrics listener resolves to a public address; metric labels never include user-supplied strings (no per-user or per-repo labels; use bounded cardinality: status codes, endpoint classes, node ID).

#### T-45 Admin CLI abuse or misuse
- Description: Admin CLI over-privileged, reachable by non-admins, or performs destructive actions without confirmation/audit.
- Impact: High.
- Likelihood: Low.
- Mitigations: CLI talks to the daemon over a unix socket with peer-credential check (`SO_PEERCRED`, uid in `forge-admin` group); every action is audited with the calling uid; destructive actions require `--yes` and print what will happen; no CLI path bypasses authz logging. Remote admin only via SSH to the node's management address over WG.

### 4.8 Abuse and content

#### T-46 Spam, abusive content, harassment
- Impact: Medium (reputation, user harm, resource cost).
- Likelihood: High.
- Mitigations: per-account rate limits on issues/comments (new accounts: 20/day; established: 200/day), per-repo "who can open issues" setting (anyone / registered users older than N days / collaborators), repo owners can lock issues and block users from their repos, admins can suspend accounts (suspension revokes all certs/keys from serving while retaining data), report link on every user-content page that creates a Titan-submitted report ticket; content hidden pending review is still available to the author and admins. Off-node shipping of audit logs (to the global leader and to an operator-controlled sink) so evidence survives node loss.

#### T-47 Illegal content and takedown workflow
- Impact: High (legal).
- Likelihood: Medium.
- Mitigations: a documented abuse contact (`abuse@` in RIPE object + `/abuse.gmi`), a takedown procedure: (1) admin marks repo/asset/issue "restricted" via CLI (immediately not served on any POP; replication of the restriction is prioritised), (2) evidence and request archived off-node, (3) owner notified, (4) after review, permanent removal via `git` ref deletion + repack with `--cruft --cruft-expiration=now` (or full deletion) and asset unlink on all nodes, with verification. Restricted objects are not fetchable even by SHA (upload-pack hides the repo entirely). Retention policy documented. Log retention limited (90 days) to reduce what we can be compelled to disclose; IPs hashed with a rotating daily salt in non-audit logs.

#### T-48 Log injection and audit log tampering
- Impact: Medium.
- Mitigations: structured logs (JSON, one line) with all user strings escaped; audit log rows are append-only in SQLite (trigger prevents UPDATE/DELETE) and shipped to the metadata leader and to an external sink; hash-chained (each row stores hash of previous) so truncation is detectable.

---

## 5. Prioritised mitigation checklist by milestone

Milestones are the planned build order (M1 skeleton daemon and Gemini read-only; M2 identity/certs; M3 SSH + git read; M4 git push + hooks; M5 Titan writes; M6 replication; M7 anycast/BGP; M8 secrets/infra automation; M9 hardening and abuse tooling; M10 pre-launch audit). Each item lists the threats it closes.

M1 - Daemon skeleton, Gemini read path
- [ ] Single `writeHeader` with CRLF/control stripping, META <= 1024 (T-15)
- [ ] Gemtext escaper with corpus tests; all templates use it (T-16)
- [ ] Route table with typed, regex-validated params; traversal fuzz tests (T-23)
- [ ] Repo/owner name regex `^[a-z0-9][a-z0-9-]{0,38}$` (T-04)
- [ ] Per-connection deadlines, request-line limits, per-IP and global connection caps (T-20)
- [ ] systemd hardening unit: `CPUQuota`, `MemoryMax`, `TasksMax`, `ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, `IPAddressDeny/Allow`, `LimitCORE=0` (T-14, T-20, T-35)
- [ ] Rendering package has no network imports (depguard test) (T-14)
- [ ] Git subprocess wrapper: allowlisted env, `GIT_CONFIG_NOSYSTEM`, `GIT_DIR` explicit, `-c` config only, `core.hooksPath` fixed, `protocol.*.allow=never`, timeouts, rlimits (T-03, T-04, T-07)
- [ ] Blob/diff render caps and binary detection; symlink entries never followed (T-09, T-13)
- [ ] Unicode escaping of filenames (T-17)
- [ ] Metrics bound to private address with startup self-check (T-44)

M2 - Identity (client certs), accounts, SQLite
- [ ] Cert identity = SHA-256(DER); revocation checked per request; expiry enforced (T-12)
- [ ] Parameterised SQL only, `trusted_schema=0`, WAL, integrity check at boot, single-opener lock (T-25, T-26)
- [ ] Central `Can()` authz with route coverage test; identical responses for missing vs forbidden (T-10, T-18)
- [ ] Registration rate limits and starter quotas / invite mode (T-29)
- [ ] Multiple certs per account; SSH-key-signed recovery challenge; admin recovery procedure documented (T-28)
- [ ] Append-only, hash-chained audit table (T-48)

M3 - SSH server, git read
- [ ] `x/crypto/ssh` server: pubkey only, exec only, no pty/shell/subsystem/forwarding/env/agent; strict exec grammar; `MaxAuthTries=3`; algorithm allowlist; ed25519 host key (T-05)
- [ ] Account from authenticated key fingerprint, not username (T-05)
- [ ] `uploadpack.allowAnySHA1InWant=false`, `hideRefs`, protocol v2 (T-07, T-10)
- [ ] Session/IP limits, idle and absolute timeouts (T-05)

M4 - git push, hooks, quotas
- [ ] `receive.fsckObjects`, `transfer.fsckObjects`, `core.protectNTFS/HFS`, `receive.maxInputSize`, `transfer.unpackLimit`, `gc.auto=0`, `core.bigFileThreshold` (T-02, T-06, T-07)
- [ ] pre-receive: per-ref authz, protected refs, ref name validation, ref count cap, quarantine size + quota check, blob size cap, tree scan for `.git*`/NTFS aliases/`.gitmodules` symlinks/option-injection (T-01, T-02, T-06, T-07)
- [ ] `core.logAllRefUpdates=true`, reflog retention for restore (T-11)
- [ ] Scheduled repack/gc/fsck with locking, low priority (T-08)
- [ ] Optional signed-commit enforcement using DB-generated allowed signers (T-11)
- [ ] Free-space watermarks and write refusal ordering (T-06, T-26)

M5 - Titan writes
- [ ] No Gemini request mutates state (route table assertion) (T-19)
- [ ] Titan token issuance/validation: 128-bit, hashed, bound, expiring, single-use for destructive actions (T-19, T-21)
- [ ] Size limits before body read, exact-N read, per-endpoint MIME allowlist, UTF-8 validation, sniffing, atomic `O_TMPFILE`+rename, quota pre/post checks, content-addressed asset store (T-22, T-24)
- [ ] `openat2(RESOLVE_BENEATH)` for data-root file access (T-23)
- [ ] Sensitive-action confirmation + notification (T-12)
- [ ] Reporting, locking, blocking, suspension, takedown "restricted" flag (T-46, T-47)

M6 - Replication (leader/replica over WireGuard)
- [ ] Per-node WG keys, exact `AllowedIPs`, services bound to wg0 only, nftables (T-33)
- [ ] Signed leader manifests; replica verification and scrub; fetch with fsck and restricted refspec (T-34, T-38, T-42)
- [ ] Replicas: no local metadata writes; no receive-pack in v1 (reject with leader hint) (T-27, T-37)
- [ ] Forwarded Titan writes: node signature + full client cert DER + token; leader re-authz; nonce/timestamp window (T-37, T-41)
- [ ] Cross-POP drift monitor (T-34)
- [ ] Audit log shipping off-node (T-46, T-48)

M7 - Anycast / BGP
- [ ] RPKI ROAs (maxLength = prefix length), IRR objects, PeeringDB (T-30)
- [ ] BIRD export = exactly our prefixes; import limited/blocked; MD5/TCP-AO; config validated in CI (T-31)
- [ ] Health-gated announcement/withdrawal; drain procedure (T-32)
- [ ] BGP monitoring alerts (T-30, T-31)
- [ ] External TOFU-pin monitors from non-anycast vantage points (T-35)

M8 - Secrets and infra automation
- [ ] `.sops.yaml` path rules; per-node age keys; CI policy check on recipients (T-39)
- [ ] TLS/SSH keys decrypted to tmpfs only; LUKS at rest; no provider snapshots (T-35, T-36)
- [ ] Cloud tokens least-privilege and off-node; OpenTofu state encrypted; DNSSEC; 2-person review for `infra/` (T-39, T-43)
- [ ] Admin CLI over unix socket with peer creds and audit (T-45)

M9 - Hardening and abuse tooling
- [ ] Reproducible builds, `govulncheck`, pinned toolchain, `-mod=readonly`, release signing, git version floor check (T-40)
- [ ] TLS/SSH key rotation runbook and `/.well-known/forge/tls-rotation.gmi` (T-35, T-36)
- [ ] Compromised-POP runbook (T-37)
- [ ] Backups (repos, DB) with restore tests (T-08, T-26)
- [ ] Rate-limit tuning, spam controls, abuse contact published (T-46, T-47)

M10 - Pre-launch audit
- [ ] Execute the full security test list (section 7); external review of SSH exec handler, Titan parser, pre-receive scanner, authz
- [ ] Threat model re-review; residual risk sign-off

---

## 6. Security invariants

Each statement is intended to be enforced by a test or a static check, and to be true on every commit.

- I-1 The SSH server never spawns a shell, allocates a PTY, opens a subsystem, or forwards a port, agent, or X11 connection.
- I-2 The SSH exec handler runs only `git-upload-pack` or `git-receive-pack`, with a path derived solely from a validated `<owner>/<repo>` pair.
- I-3 The account for an SSH session is derived from the key that authenticated, never from the username.
- I-4 No Gemini (non-Titan) request changes persistent state.
- I-5 Every Titan write requires a registered, unrevoked, unexpired client certificate and a valid token bound to that certificate and purpose.
- I-6 The daemon never checks out a worktree and never follows a symlink recorded in a git tree.
- I-7 Every git subprocess is started with an allowlisted environment, explicit `GIT_DIR`, `GIT_CONFIG_NOSYSTEM=1`, `core.hooksPath` set to the fixed hooks directory, `protocol.*.allow=never` (except the replication fetch's single allowed transport), a timeout, and resource limits.
- I-8 Git configuration for a repository is never read from files a user can write; all settings are passed with `-c` or environment.
- I-9 `receive.fsckObjects`, `transfer.fsckObjects`, `fetch.fsckObjects`, `core.protectNTFS`, and `core.protectHFS` are on for every receive and fetch.
- I-10 No shell is ever invoked by the daemon (`sh`, `bash`, `/bin/sh -c`); argv is always built as a slice.
- I-11 Every SQL statement uses bound parameters; no user string is interpolated into SQL text.
- I-12 Every route passes through the central authorisation function; a nonexistent and a forbidden resource yield byte-identical responses.
- I-13 Every Gemini response header is written by one function that rejects control characters and enforces META length; user text never reaches a header.
- I-14 A replica node cannot cause a ref update on a leader without a valid, unexpired user credential the leader verifies itself.
- I-15 A replica node cannot modify global metadata (accounts, keys, certs, ACLs, leadership).
- I-16 Each node holds exactly one WireGuard private key, generated on that node, and peers accept traffic only from that node's assigned mesh address.
- I-17 Cloud provider, DNS, and RIR credentials are never present on any POP node.
- I-18 The TLS and SSH private keys exist on disk only in encrypted (SOPS) form; at runtime they live only in tmpfs.
- I-19 Every file path opened under the data root is constructed from validated tokens and opened with `RESOLVE_BENEATH`/`O_NOFOLLOW`; the data root contains no symlinks.
- I-20 All user content rendered into gemtext passes through the escaper; user content cannot open a preformatted block, emit a heading at page level, or produce an unmarked link line.
- I-21 Titan body size is bounded before any byte of the body is read, and no partial upload is ever visible at its final path.
- I-22 The rendering package has no dependency capable of network I/O.
- I-23 The metrics endpoint listens only on a non-public address and exposes no user-supplied label values.
- I-24 The daemon is the only process with the SQLite database open.
- I-25 Audit log rows are never updated or deleted by the daemon.
- I-26 BIRD exports exactly the configured set of our own prefixes and nothing else.

---

## 7. Required security tests

Unit / integration (run in CI on every PR):
1. SSH conformance: attempt `shell`, `pty-req`, `subsystem sftp`, `env`, `direct-tcpip`, `tcpip-forward`, `auth-agent-req`, `x11-req`, password auth, keyboard-interactive; all must be refused and the connection closed without executing anything (I-1). Fuzz the exec line parser with 10k random and traversal/injection strings (I-2, I-10).
2. SSH auth: unknown key vs revoked key vs wrong-account key produce identical errors and timing within tolerance; `MaxAuthTries` enforced (T-18).
3. Git push corpus: repository fixtures containing `.git` tree entries, NTFS short-name aliases, `.gitmodules` as symlink, submodule URL `-oProxyCommand=`, `file://` submodule, 500k refs, 1 GiB blob, 200 MiB pack that expands to 4 GiB, commit with 10 MiB message, non-fast-forward to protected branch, tag deletion by non-owner, push to `refs/forge/x`. Each must be rejected with the expected message and leave no objects outside quarantine (T-01, T-02, T-06, T-07).
4. Git config assertion: a test wraps the subprocess runner and asserts the exact argv/env for each call site (I-7, I-8, I-9); a fixture repo with a malicious `config` (`core.hooksPath`, `core.sshCommand`, `diff.external`) and `.gitattributes` filters is rendered and pushed to without any of them executing (a canary command that touches a file must not run).
5. Rendering: 100 MiB single-line blob, 50k-file diff, blob with CRLF header injection, gemtext with `=>`, `#`, `` ``` `` at line start, bidi control filenames; assert caps, escaping, and no follow of symlink entries (T-13, T-15, T-16, T-17, I-13, I-20).
6. Router fuzz: traversal (`..`, `%2e%2e`, `%00`, backslash, overlong UTF-8), assert no path outside root is ever opened (use a fake FS that records opens) (T-23, I-19).
7. Titan: `size` mismatch (short/long body), size over limit (assert no body read), MIME outside allowlist, invalid UTF-8, token reuse, expired token, token from a different cert, upload with no cert, concurrent uploads hitting quota; assert atomicity by killing the process mid-write and checking no partial file at final path (T-21, T-22, I-5, I-21).
8. Authz coverage: reflect over the route table and assert every route has an authz policy; property test that `Can()` is called for every handled request (I-12). Golden test that missing vs forbidden responses are byte-identical (T-10).
9. SQL: static grep/lint that no `Query`/`Exec` receives a formatted string; FTS query builder fuzz (I-11, T-25).
10. Crash consistency: kill -9 during a push and during a Titan commit; on restart, integrity check passes and the repo/DB are consistent (T-26).
11. Secrets: log redaction test; `.sops.yaml` recipient policy check; test that node secret bundles do not contain cloud tokens (I-17, T-39).
12. Escaper/idempotence property tests: escaping is idempotent and never produces a line starting with `` ``` `` or an unmarked `=>` from user input.

Deployment / staging (run before each release and weekly):
13. Replica isolation: from a replica, attempt to push to a leader repo, write metadata, and sign a manifest; all must fail (I-14, I-15). Forwarded write with a forged node signature, an expired token, a revoked cert, and a replayed nonce; all rejected.
14. WireGuard: a node with a foreign source address inside the tunnel is dropped; services are unreachable on public interfaces (`nmap` from outside against 9090, replication port, admin socket) (I-16, I-23).
15. BGP: `bird -p` on generated config; a test peer verifies only our prefixes are exported and a full table on import is rejected (I-26). Withdrawal on health failure within 30 s.
16. TOFU monitors from 3 external vantage points pin the TLS and SSH fingerprints; a mismatch alert path is exercised (T-35).
17. Load: slowloris, 10k idle connections, 1000 concurrent clones of a 500 MiB repo, 100 concurrent 256 MiB Titan uploads; assert the node stays responsive for a health probe and limits engage (T-20, T-06).
18. Restore drill: restore one repo and the DB from backup into a scratch node; `fsck` and integrity check pass (T-08, T-26).
19. Key rotation drill: rotate TLS and SSH keys in staging following the runbook; verify the announcement page, dual host keys, and monitor updates (T-35, T-36).
20. Compromised-POP drill: simulate by revoking one node's WG peer and age recipient; verify it loses mesh access and cannot decrypt new secret bundles; rebuild via OpenTofu (T-37).

Static / supply chain (CI):
21. `govulncheck`, `go vet`, `staticcheck`, depguard (no net imports in rendering; no `os/exec` outside the git wrapper package), `-mod=readonly`, reproducible build hash comparison between two builders, git binary version floor check in the image build (T-40).

---

## 8. Residual risks and open decisions

- Same TLS/SSH key on every POP is inherent to anycast + TOFU. Accepted with tmpfs-only runtime, encrypted storage, monitoring, and a rotation plan. Revisit if a practical remote signer for TLS appears.
- First-contact TOFU is defeated by a hijack or DNS compromise at the moment of first contact. Mitigated by out-of-band fingerprint publication; not eliminated.
- Volumetric DDoS beyond aggregate POP capacity is outside our control; anycast and provider filtering are the only levers.
- Provider (Vultr) has physical access; disk encryption does not protect against a live memory dump. Accepted.
- Open: whether to forward git pushes from replicas (proxy) after v1, and the exact attestation format; whether to allow partial clone filters; whether user gemtext links should be rendered as links at all or as plain text for new accounts.


## Amendments

- 2026-09-10 (ADR 0012): `receive-pack` sessions are accepted from accounts
  with read access so that they can push `refs/for/<branch>` and
  `refs/changes/<n>`; the pre-receive hook restricts readers to exactly those
  refs, one per push, with `max_change_bytes`, `max_open_changes_per_user`
  and `max_change_commits` limits. `receive.advertisePushOptions=true` with
  strict option validation (`topic`, `change`, `title` only) and
  `receive.procReceiveRefs` for `refs/for` and `refs/changes`.
- 2026-09-10 (T-19/T-21 deviation): Titan writes are authorised by the client
  certificate alone; no single-use token. A Titan upload is an explicit
  client action, never a link click, and the affected clients drop URL
  parameters from links anyway. State changes reachable over plain Gemini
  require an INPUT-typed confirmation. Revisit if abuse is observed.
- 2026-09-10 (security review SR-01): INPUT-driven state changes require a
  per-identity action token in the path (`docs/titan.md`, "Action tokens");
  the earlier "typed confirmation" mitigation was insufficient because links
  can pre-fill queries. Invariant I-20 amended: README links are emitted with
  a `[readme link]` marker and repository gemtext is served raw as
  `text/plain`.
