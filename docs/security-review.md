# Security review: implementation vs threat model and ADR 0012

Date: 2026-09-10. Scope: `internal/{gemini,web,forge,hooks,sshd,vcs/git,store,repl,health}`,
`cmd/forge`, `migrations`, `scripts/{deploy,secrets,bgp-announce}`,
`infra/cloud-init/node.yaml.tftpl`, `infra/systemd/*.service`,
`infra/firewall/nftables.conf.tftpl`, `.sops.yaml`. Reviewed against
`docs/threat-model.md` sections 6 and 7 and the ADR 0012 permission matrix.
Method: full read of every file above, targeted verification by test where
cheap, and Go native fuzzing of the parsers and renderers (section 4).

Nothing in this review modifies non-test code. Patches are proposals for the
lead to apply. Fuzz tests added: `pkg/gemini/fuzz_test.go`,
`internal/hooks/fuzz_test.go`, `internal/sshd/fuzz_test.go`,
`internal/web/fuzz_test.go`, `internal/vcs/git/fuzz_test.go`,
`internal/forge/fuzz_test.go` (the last one because `parsePushOptions` is
unexported and can only be fuzzed in-package). Invariants that document open
findings are gated behind `FORGE_FUZZ_STRICT=1` so CI stays green until the
patch lands; each gate names its finding ID.

Note: `go build ./...` currently fails in `internal/vcs/hg` (`runner.go:370:
undefined: Repo`), outside this review's scope; everything else builds and
vets.

## 0. Status (2026-09-10, after remediation)

Applied on main: SR-01 (per-identity HMAC action tokens in the path, see
`docs/titan.md` "Consent rule"), SR-03 (peer name bound to its configured
control address; forwarded writes documented as trusting the replica's TLS
verification), SR-04 (refs of changes closed for 90 days pruned by
maintenance; `refs/changes/*` replicated), SR-05, SR-06, SR-07, SR-08, SR-09,
SR-10 (16-byte codes, per-IP enrolment limiter), SR-11, SR-12, SR-13, SR-14,
SR-15, SR-16, SR-17, SR-18, SR-19c, SR-20a, SR-21, SR-22, SR-23, SR-25.
Also applied: SR-02 (secrets decrypted only into `/run/forge` by
`forge-secrets.service`; the node age key is the sole persistent secret),
SR-19a (deploy sudo limited to `forge-deploy-helper`), SR-19b (pinned host
keys in `infra/known_hosts`, `StrictHostKeyChecking=yes`), SR-20b
(`uploadpackfilter` limited to blob:none, blob:limit and tree depth 3).
Open: SR-24 (informational).

## 1. Summary

The implementation is in good shape on the classic injection surfaces: every
SQL statement is parameterised, every response header goes through
`SanitizeMeta`, git argv is built from validated tokens with
`--end-of-options`, the SSH server refuses everything but one exec of
`upload-pack`/`receive-pack`, the hook identity comes from the SSH server's
environment and cannot be influenced by the client, and Titan writes require a
registered certificate before any byte of the body is read.

The important gap is design-level: **every state change reachable over plain
Gemini is driven by the URL query, and a link can carry a query.** The
"INPUT-typed confirmation" the threat model amendment relies on does not exist
as a mechanism: `req.Query()` is the same whether the user typed it into an
INPUT prompt or clicked a link that already contained it. User content
(issue/comment/review bodies, README files) may contain links to the forge's
own host, so a logged-in user who clicks a planted link can have an
attacker's SSH key added to their account or an attacker granted admin on
their repository (SR-01). This is the one High finding; the rest are Medium
or Low and mostly concern deviations from the invariants that should either
be fixed or written down as accepted.

## 2. Findings

Severity: High = account/repository takeover or integrity loss by a remote
unprivileged party; Medium = integrity/availability loss needing preconditions
or an invariant not met; Low = hardening, robustness, policy drift; Info =
observation, no action required.

| ID | Sev | Component | Location | Description | Exploit sketch | Fix |
|----|-----|-----------|----------|-------------|----------------|-----|
| SR-01 | High | web (Gemini INPUT routes) | `internal/web/account.go:161-176` (SSH key add), `:129-146` (profile), `:241-263` (cert revoke), `:86-107` (enrol); `internal/web/settings.go:37-108` (description, visibility, archive, branch, collaborators add/remove, delete); `internal/web/issues.go:186-216` (close/reopen), `:315-329` (comment delete); `internal/web/releases.go:57-70`, `:82-97` (release/asset delete); `internal/web/front.go:60` (repo create) | Every state-changing Gemini route acts on `req.Query()` with no proof the value came from an INPUT prompt. A link with a pre-filled query performs the action on one click. User content links are rendered (marked `[user link]`, `issues.go:334-367`) and README links are rendered unmarked (SR-07), both may point at the forge's own host. The "type delete to confirm" pattern gives no protection: the link supplies the word. | Attacker posts a comment containing `=> /account/keys/add?ssh-ed25519%20AAAAC3...%20x See the log` (label chosen freely). A logged-in victim clicks: `AddSSHKeys` runs, the attacker can now push as the victim. Same for `=> /~victim/repo/settings/collaborators/add?attacker%20admin`, `.../settings/visibility?public`, `.../settings/delete?victim/repo` (soft delete, 7 days), `/account/certs/revoke/<spki>?revoke`. | Bind every INPUT action to an unguessable per-user path token and redirect query-carrying requests that lack it to the tokenised path (which drops the query, so the client shows the INPUT prompt and the user must type). Patch P-1. Alternatively make SSH-key and collaborator changes Titan-only. Update the T-19 amendment: INPUT is not a confirmation mechanism. |
| SR-02 | Medium | infra / deploy | `scripts/deploy:146-168`, `infra/cloud-init/forge.toml.tftpl:17-24`, `infra/systemd/forge.service` | I-18 not met: TLS key, SSH host key, cluster secret, WireGuard key and BGP password are decrypted onto the persistent root filesystem (`/etc/forge/secrets`, `/etc/wireguard/wg0.conf`, `/etc/bird/bird.conf`). Only the transport to the node is encrypted. | Disk image or snapshot of a POP (provider access, decommissioning) yields the anycast-wide TLS and SSH private keys. | Decrypt into tmpfs at boot (`/run/forge/secrets`, `RuntimeDirectory=forge`, a root `forge-secrets.service` running `age -d` before `forge.service`, `LoadCredential=` for the forge unit), keep only `secrets.tar.age` on disk; or amend I-18 to state the accepted deviation. Patch P-11. |
| SR-03 | Medium | repl (forwarded writes) | `internal/repl/auth.go:29-40`, `internal/repl/forward.go:76-96`, `internal/web/forward.go:46-64` | The leader re-authorises a forwarded write from the client *certificate DER*, which is public (every server the user ever connected to has it) and is not proof of key possession. The cluster secret is shared and the `X-Forge-Node` name is not bound to the caller's address. Any process on the mesh holding the secret and a writer's cert DER can execute any Titan write as that user, including `merge`, which moves `refs/heads/*`. I-14 ("valid user credential the leader verifies itself") is only nominally met; I-15 holds (metadata is pull-only from the configured leader). | Compromised replica (T-37) replays cert DERs it saw and merges/closes changes, edits issues, uploads assets on every repository the users can write to. | Accept and document (Gemini cannot sign requests). Cheap hardening: check `r.RemoteAddr` host against `Peers[peer]` (patch P-12), per-node secrets, audit-log forwarded writes with the origin node, and rate-limit `/v1/forward`. Revisit "push forwarding after v1" with this in mind. |
| SR-04 | Medium | forge (change pushes) | `internal/forge/hooks.go:66-77`, `internal/forge/changes.go:69-234` | Reader pushes to `refs/for/*` are charged to the *owner's* repository and account quota (`MaxRepoBytes`, `MaxUserBytes`) and the objects stay reachable forever via `refs/changes/<n>/v<k>` (never deleted, no purge, no user blocking despite ADR 0012 section 10 saying "repository owners can block users"). | Any registered account pushes 10 changes x 64 MiB of junk to a public repository (more with several accounts): the owner hits `MaxRepoBytes`/`MaxUserBytes` and can no longer push to their own repository; there is no way to remove the objects. | Do not charge reader pushes to the owner's `MaxUserBytes`; add a per-repository reader-contribution budget; give owners/admins a way to purge closed changes (delete `refs/changes/<n>/*`, gc) and implement blocking. Patch P-7. |
| SR-05 | Medium | forge (change creation) | `internal/forge/changes.go:162-173` | Title and body of a new change come from the oldest commit's subject/body without `checkText`: body size is unbounded (a 10 MiB commit message becomes a 10 MiB `changes.body` row rendered on every change page), UTF-8 is not validated, NUL is not rejected, `title[:MaxTitleLen]` cuts inside a rune. The threat model's test corpus ("commit with 10 MiB message") expects rejection. | `git commit -m "$(head -c 10000000 /dev/urandom)"; git push origin HEAD:refs/for/main`. | Run `checkText` (or truncate body to `MaxTextBytes` on a rune boundary) and fail the push with a clear reason. Patch P-3. |
| SR-06 | Medium | web (review anchors) | `internal/web/changes.go:289-323` (line 316) | `renderAnchored` turns `@ <path>[:line]` into an **unmarked** link `=> /~o/r/changes/<n>/v<k>/diff/<path> ...`. `path` is `\S+` and is neither validated nor escaped: `..` segments, `?` and `#` are accepted, so the link resolves anywhere on the host, with a query. Violates I-20 and is a delivery vehicle for SR-01 without the `[user link]` marker. | Review body `@ ../../../../../account/keys/add?ssh-ed25519%20AAAA...:1` renders as `=> /~o/r/changes/7/v1/diff/../../../../../account/keys/add?ssh-ed25519%20AAAA... ../../..:1 (v1)`. | Validate the anchor path (relative, no `.`/`..`/empty segments, no `?#%` or control characters, length cap) and percent-escape each segment; otherwise render the line as text. Patch P-2. |
| SR-07 | Low | web (README rendering) | `internal/web/repo.go:266-270`, `internal/web/markdown.go:35`, `:130-146` | README.md / README.gmi links are emitted as ordinary link lines (no `[user link]` marker), to any scheme, including host-relative paths. Deviation from I-20 ("cannot produce an unmarked link line"). | A repository README carrying `[changelog](/account/keys/add?...)`. | Either mark README links or restrict README link targets to relative paths inside the repository plus `gemini://`/`https://`. Becomes moot for state changes once P-1 lands; still worth marking. |
| SR-08 | Low | gemini / git | `pkg/gemini/request.go:83-126`, `internal/web/handler.go:115-119`, `internal/vcs/git/runner.go:363-382`, `internal/vcs/git/repo.go:300` | The request line is checked for control characters *before* percent-decoding; `%00`, `%0d%0a`, `%09` survive into `u.Path`. `web` rejects NUL but not CR/LF; `checkPath` accepts control characters, so a path with `\n` is written into `git cat-file --batch` stdin as a second request line (`repo.go:300`). No privilege gain found (the second line is ignored and every object is readable anyway), but it is exactly the class T-23 asks to reject "after decoding once". Found by `FuzzParseRequestLine` (strict). | `gemini://host/~o/r/raw/main/README%0aHEAD:x`. | Reject `< 0x20`/`0x7f` in the decoded path in `parseRequestLine` and in `checkPath`. Patch P-4. |
| SR-09 | Low | forge (push options) | `internal/forge/changes.go:35-65` | `-o title=` is length-checked only; a raw pkt-line client can send control characters and invalid UTF-8 (git's own client refuses newlines, the server does not). Stored unvalidated; feeds/XML get it. | Custom client sends push option `title=a\x01b\xff`. | Reuse `checkText(title, "")` and reject control characters. Patch P-3. |
| SR-10 | Low | web / forge (accounts) | `internal/web/account.go:48-107`, `:221-240`; `internal/forge/identity.go:150-163` | No per-IP limiter on registration, enrolment attempts or INPUT-driven writes (only Titan writes are limited, per user). Enrolment codes are 64-bit (8 bytes hex), 15 min TTL, single use (`ConsumeToken` is atomic, `store/users.go:306`). 64 bits is far beyond online guessing at any plausible rate, but the threat model specifies 128-bit tokens. `enrol-code` generation is itself a GET with a side effect. | Account spam: a script with fresh self-signed certs registers unlimited accounts, each may create 100 repositories. | Per-IP token bucket for `/account` writes and `/new`; 16-byte codes; require a typed confirmation (or P-1 token) before minting an enrolment code. Patch P-9. |
| SR-11 | Low | forge (archived repos) | `internal/forge/issues.go:90-112`, `:170-196`; `internal/forge/changes.go:331-353`; `internal/forge/releases.go:89-125` (`EditRelease`, `DeleteRelease`), `:129-182` (`AddAsset`, `RemoveAsset`) | ADR 0012 section 10: "Archived repositories: no pushes, no Titan writes except close." `EditIssue`, `SetIssueState` (reopen), `EditComment`, `DeleteComment`, `EditChange`, `EditRelease`, `DeleteRelease`, `AddAsset`, `RemoveAsset` do not check `Archived`. | Writer keeps editing/uploading into an archived repository. | Add the `Archived` check to each. Patch P-6. |
| SR-12 | Low | web (markdown) | `internal/web/markdown.go:100-102` | An indented code line whose content starts with ``` emits three fence lines in a row: the rest of the README, the commit list and the footer are swallowed into a preformatted block. Found by `FuzzMarkdownToGemtext` (strict). | README containing four spaces followed by ```. | Prefix such lines with a space, as `Page.Pre` does. Patch P-5. |
| SR-13 | Low | web (markdown) | `internal/web/markdown.go:35`, `:120-125` | A link whose target `resolveLink` drops (`#fragment`, `javascript:`, `data:`) is still emitted as `=>  label`, which clients parse as a link to the URL `label`. | `[click](#top)` in a README yields a link to `/~o/r/tree/main/click`. | Emit the label as text when the target is empty. Patch P-5. |
| SR-14 | Low | web (raw blobs) | `internal/web/repo.go:470-472` | `.gmi`/`.gemini` blobs are served as `text/gemini`; T-24 says raw blobs are never served as gemtext. Serving user gemtext is the same class of risk as HTML with links: user-authored link lines, no marker. | Repository file `x.gmi` with `=> /account/keys/add?...`. | Serve raw as `text/plain` and render gemtext through the same marking path as READMEs, or document the deviation. |
| SR-15 | Low | forge (post-receive) | `internal/forge/hooks.go:188-206` | `countCommits` pages `git log --skip=N` in 1000s until it meets `old`; on a non-fast-forward push (`old` unreachable from `new`) it walks the whole history, O(n^2/1000) subprocess work, bounded only by the 50 s hook context. Writer-only. | Force-push on a 500k-commit repository. | Use `repo.CountCommits(ctx, old, new)` (`rev-list --count old..new`). Patch P-8. |
| SR-16 | Low | forge (comments) | `internal/forge/issues.go:174`, `:192` | Any repository *writer* can edit and delete anyone's comment; the matrix says writer: own, admin: any. | Collaborator with write role deletes the maintainer's comments. | `c.AuthorID != u.ID && !acc.CanAdmin()`. Patch P-6. |
| SR-17 | Low | forge (text limits) | `internal/forge/issues.go:44` | `checkText` rejects NUL in the body but not in the title. | Title with `\x00` stored and rendered. | Check the title too. Patch P-3. |
| SR-18 | Low | cmd (SSH auth) | `cmd/forge/ssh.go:31` | `TouchSSHKey` runs in the public-key callback, which x/crypto/ssh invokes for the *query* phase before any signature is verified. Anyone can offer a victim's public key and stamp `last_used_at`; it is also one DB write per probe. | Offer public keys scraped from elsewhere. | Move the touch into the session (after `sconn.Permissions` is established) or into `Authorize`. Patch P-10. |
| SR-19 | Low | infra | `infra/cloud-init/node.yaml.tftpl:53`, `scripts/deploy:271`, `:27`; `infra/systemd/forge-backup.service:21` | (a) `deploy ALL=(ALL) NOPASSWD:ALL` (already marked TODO); (b) `StrictHostKeyChecking=accept-new` makes the first deploy TOFU; (c) `forge-backup.service` sets `ReadOnlyPaths=/var/lib/forge` while `forge admin backup` writes its SQLite snapshot and bundles under `DataDir/tmp` (`internal/forge/backup.go:37`), so the timer job will fail as written. | (a) any compromise of the deploy key is root on every POP. | (a) restrict via `sudoers` command list once the remote helper settles; (b) pin host keys from the provider console / SSHFP; (c) `ReadWritePaths=/var/lib/forge/tmp` or point the snapshot at `/var/backups/forge`. |
| SR-20 | Low | git env | `internal/vcs/git/runner.go:116`, `:121`, `:325-326`; `internal/repl/git.go:202` | (a) `protocol.file.allow=always` for *every* subprocess; I-7 wants `never` except the replication transport (which is HTTP and already enables itself in `repl/git.go`). (b) `uploadpack.allowFilter=true` with no `uploadpackfilter.*` limits (open question in the threat model); `sparse:oid` and deep `tree:` filters are CPU-expensive per request. (c) Replication fetches only `refs/heads/*` and `refs/tags/*`; ADR 0012 requires `refs/changes/*` too, so replicas cannot serve change refs and `move-leader` compares only heads/tags. | (b) 16 concurrent `--filter=sparse:oid=...` clones. | (a) drop `protocol.file.allow=always` (restore/bundle paths use `--` and local paths, which git allows regardless) or scope it to `Restore`; (b) `uploadpackfilter.sparse:oid.allow=false`, `uploadpackfilter.tree.maxDepth=2`; (c) add `+refs/changes/*:refs/changes/*` to both fetch refspecs. |
| SR-21 | Info | sshd | `internal/sshd/command.go:51`, `internal/forge/forge.go:75` | `repoRe` accepts `..` inside a name (`repo.o..000000`), `ValidRepoName` rejects it; the SSH name is only a database key so nothing is reachable, but the grammars should agree. Found by `FuzzParseCommand`. | none | Add `strings.Contains(repo, "..")` to `ParseCommand`. |
| SR-22 | Info | store (feeds) | `internal/store/events.go:97` | Events without a repository (`user.create`, `user.key.add`, `user.cert.add`) are visible to everyone in the global feed, revealing when a user adds keys or devices. | none | Restrict `user.*` events to the user's own view or drop them from public feeds. |
| SR-23 | Info | web (user links) | `internal/web/issues.go:347` | `titan://` targets are allowed in user links; no legitimate use, and it invites upload phishing. | none | Drop `titan://` from the allowlist. |
| SR-25 | Info | forge (text) | `internal/forge/issues.go:21-34` | `SplitTitleBody` only normalises CRLF, so a lone CR stays inside the title (`"0\r0"`), and it strips `# ` before trimming whitespace, so a title line starting with CR keeps its `#` (`"\r#000"`). `checkText` rejects CR in titles for issues, changes and releases, so this is cosmetic. Found by `FuzzSplitTitleBody`. | none | Normalise lone CR to LF (or strip it) and `strings.TrimLeft(strings.TrimSpace(lines[i]), "# ")`. |
| SR-24 | Info | hooks | `internal/hooks/hooks.go:97-104`, `:172-186` | Pre-receive input is read with a default `bufio.Scanner` (64 KiB tokens); safe only because pkt-line bounds a command at 65 516 bytes. Hook socket is `chmod 0600` after `Listen` (tiny window at umask). Requests over the socket are unauthenticated: any process running as the `forge` user can forge `pre-receive`/`proc-receive` requests (accepted: same-user processes are already fully trusted). | none | `sc.Buffer(make([]byte, 128<<10), 128<<10)`; set umask or create the socket in a 0700 directory. Document the same-user trust boundary. |

Verified and found correct (no finding):

- SQL: every `Query`/`Exec` uses bound parameters. The only dynamic SQL is placeholder numbering in `store.Events` (`itoa`), the table name in `touchTarget` (two-way switch), and the replication snapshot (`store/repl.go`): table names come from the `repoTables`/`globalTables` allowlists, column names are checked against `pragma_table_info`, and the `NOT IN (...)` list is built from `toInt64` values. Remote JSON keys cannot reach SQL text.
- META/CRLF: the only header writers are `responseWriter.Header` and `captureWriter.Header`, both call `SanitizeMeta`. Every user-influenced META (redirect targets built from path segments, error messages) passes through it. I-13 met; fuzzed.
- Titan: `serveTitan` calls `requireUser` first; `readTitanText` and `AddAsset` check `Titan.Size` against the endpoint limit before reading; the server rejects `size > MaxTitanBody` before the handler runs; MIME allowlists for text (`text/plain|gemini|markdown`) and assets; `;edit` is served only after `LookupRepo`, so private issue/change/release text is only returned to readers. Forwarded writes cap the body at `MaxTitanBytes` and are re-authorised on the leader. Assets are written to a temp file, fsynced, then renamed (I-21).
- Authorisation: `LookupRepo` maps `RoleNone` to `ErrNotFound` (51), identical to a missing repository, over Gemini, Titan and SSH (`ErrNoRepo`). Readers pushing `refs/heads/*` are refused in `preReceive`; `refs/changes/<n>` by a non-author non-writer is refused in `procReceive`; direct pushes to `refs/changes/<n>/v<k>` or `/head` are refused (`isChangeRef` requires a bare number, and git hands every `refs/changes/*` command to proc-receive). Author cannot approve own change (`counts` false, verdict demoted). Merge is writer-only with a compare-and-swap on the target ref. Archived repositories refuse pushes at SSH authorisation and pre-receive.
- SSH: public keys only (RSA >= 2048, no certificates), modern KEX/cipher/MAC lists, ed25519 host key, `env` accepts only `GIT_PROTOCOL=version=2`, `shell`/`pty-req`/`subsystem`/forwarding/global requests refused, one exec per session, `upload-pack --strict --timeout`, session and idle timeouts, per-IP and total connection caps, `FORGE_ACCOUNT_ID` set only from the authenticated key's permissions. `ParseCommand` never shell-splits; fuzzed (SR-21 aside).
- Git environment: `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_ATTR_NOSYSTEM=1`, fixed `PATH`/`HOME`, `core.hooksPath` fixed, `core.protectNTFS/HFS`, `receive/transfer/fetch.fsckObjects` (covers `.git` entries, `.gitmodules` symlinks, NTFS/HFS aliases), `receive.maxInputSize`, `protocol.allow=never` (except SR-20a), concurrency semaphore, per-command timeout, stdout/stderr caps. No shell is invoked by the daemon; the hook scripts (`cmd/forge/serve.go:301`) are `/bin/sh` wrappers with a fixed, `%q`-quoted argv executed by git, not by the daemon.
- Replication: constant-time secret compare, peer allowlist, `git-receive-pack` answers 403, `info/refs` requires `service=git-upload-pack`, `{owner}/{repo}` cannot contain `/` or `\` or start with `.` and must resolve through `RepoByPath`, JSON bodies capped at 64 MiB, `control_listen` documented as mesh-only and nftables admits `PRIVATE_TCP` only from `wg0`.
- Secrets: no key material in the tree (grep for `age1`, `AGE-SECRET-KEY`, `BEGIN ... PRIVATE KEY`, cloud tokens: only placeholders and the schema example). `.sops.yaml` recipients are placeholders (operator only). CI greps for private keys and requires `sops:` metadata in bundles. Generated host/TLS keys are written 0600 with `O_EXCL`. Deploy quoting: remote commands take fixed strings or positional parameters via `sh -s --`; `perl` substitution reads values from the environment.
- DoS: Gemini 1024 total / 32 per IP, SSH 256 / 16, nftables 60 new connections per minute per source, read/write/body deadlines, git subprocess cap of 16 with 10 s slot wait, blob/diff/patch/log output caps, `ListRepos`/`ListIssues`/`ListChanges` limits, `changeFeed` scans at most 500 events, Go regexps are linear-time (no catastrophic backtracking possible; `mdInline` and `anchorRe` fuzzed).

## 3. Invariant verdicts

| Invariant | Verdict | Evidence |
|-----------|---------|----------|
| I-1 no shell/PTY/subsystem/forwarding | Met | `sshd/session.go` refuses `shell`, `pty-req`, `subsystem`, all other requests; non-session channels rejected; global requests discarded. Tests `TestShellAndNonGitExecRefused`, `TestSubsystemRefused`, `TestLocalPortForwardRefused`, `TestRemotePortForwardRefused`, `TestGoClientRestrictions`. |
| I-2 exec only upload/receive-pack on validated owner/repo | Met | `ParseCommand` grammar, `Authorize` returns the on-disk path from the DB record; `FuzzParseCommand`. |
| I-3 account from key, never username | Met | Login name ignored; `accountFromPermissions` reads only `sconn.Permissions`. |
| I-4 no Gemini request changes state | Not met (by amendment) | 17 INPUT-driven routes change state (SR-01 list). The amendment substitutes "INPUT-typed confirmation", which is not enforceable and is bypassed by links (SR-01). |
| I-5 Titan writes need registered, unrevoked, unexpired cert | Met (token part dropped by amendment) | `Authenticate` checks `NotAfter`, revocation, disabled; `serveTitan` requires a user before reading. No token (documented deviation). |
| I-6 never checkout, never follow tree symlinks | Met | All reads via `cat-file`/`ls-tree`; symlink entries are rendered as their target text; merges use `merge-tree --write-tree`. |
| I-7 hardened git env | Partially | All items present except `protocol.file.allow=always` on every subprocess (SR-20a). Timeouts and caps present. |
| I-8 no repo config from user-writable files | Met | Bare repos, hooks dir removed at init, users have no filesystem access; `Init` writes only `core.*`/`gc.*` keys. |
| I-9 fsck + protectNTFS/HFS | Met | `runner.go:99-104`; `TestEnvProcReceiveConfig`. |
| I-10 no shell invoked | Met | argv slices everywhere; hook wrapper scripts are executed by git with fixed argv. |
| I-11 SQL bound parameters | Met | Full read of `internal/store`; see section 2 notes. No CI lint yet (required test 9). |
| I-12 central authz; missing == forbidden | Met | `LookupRepo` in every Gemini/Titan repo route and in SSH `Authorize`; both cases yield 51 / "not found". No golden test yet (required test 8). |
| I-13 single header writer, META sanitised | Met | `SanitizeMeta` in both writers; fuzzed. |
| I-20 user content escaped; no pre/heading/unmarked link | Partially | `Page.Text`/`Pre` correct (fuzzed). `userGemtext` deliberately allows fences and marked links (documented design). Violations: SR-06 (unmarked anchor links, path unescaped), SR-07 (README links unmarked), SR-14 (raw `.gmi`). |
| I-14 replica cannot move leader refs without a user credential | Partially | Forwarded `merge` moves a branch on the strength of a cert DER, which is not a possession proof (SR-03). |
| I-15 replica cannot modify global metadata | Met | Global metadata is pulled only from the configured metadata leader; `/v1/users` answers 409 elsewhere. |
| I-16 one WG key per node, generated on node | Not met (deviation) | WG private keys are generated by the operator (`scripts/secrets gen-wg`) and shipped in the bundle. Node age keys *are* generated on the node. |
| I-17 no cloud/DNS/RIR credentials on POPs | Met | `push_secrets` extracts eight named keys only; `vultr_api_key`/`cloudflare_api_token` are used via `secrets env` on the operator machine. |
| I-18 keys on disk only encrypted, tmpfs at runtime | Not met | SR-02. |
| I-19 paths from validated tokens, RESOLVE_BENEATH | Partially | Paths are built from DB values validated by regex at creation; asset names validated; no `openat2`/`O_NOFOLLOW` is used (plain `os.Open`). Acceptable given the daemon never creates symlinks, but the invariant text should say so. |
| I-21 Titan size bounded before body read; no partial file visible | Met | `server.go:249`, `readTitanText`, `AddAsset` temp+rename. |
| I-22 rendering has no network deps | Met | `web` imports only stdlib and internal packages; no depguard check in CI (required test 21). |
| I-23 metrics private, no user labels | Met | `metrics_listen` on wg0, nftables; labels are peer names. |
| I-24 daemon only DB opener | Met (operationally) | `forge admin` opens the same DB while the daemon runs (busy timeout, WAL); `restore` refuses when the socket exists. Not an invariant the code can enforce. |
| I-25 audit rows never updated/deleted | Met | No `UPDATE`/`DELETE` on `events` except the replication `INSERT OR IGNORE`. |
| I-26 BIRD exports exactly our prefixes | Not reviewed | Generated BIRD configs are out of this review's file list. |

Required tests (threat model section 7) present in the tree: 1 (SSH conformance, exec fuzz now added), 2 partially (`TestUnknownKeyRejected`, no timing test), 4 partially (`TestEnvProcReceiveConfig` asserts env, no malicious-config fixture), 5 partially (`TestPageEscaping`, now fuzzed), 6 partially (`TestTraversalRejected` for SSH; Gemini router fuzz now via `FuzzParseRequestLine`/`FuzzCheckPath`), 12 (now `FuzzPage`/`FuzzUserGemtext`). Missing: 3 (push corpus: `.git` entries, `.gitmodules` symlink, 10 MiB message, 500k refs), 7 (Titan matrix: size mismatch, no cert, MIME, quota race, kill mid-write), 8 (authz reflection / missing-vs-forbidden golden), 9 (SQL lint), 10 (crash consistency), 11 (log redaction), 13-20 (deployment drills), 21 (depguard, reproducible build).

## 4. Fuzzing

Go native fuzzing, `go test -run='^$' -fuzz=<target> -fuzztime=20s`, on
2026-09-10 (git 2.43, Go 1.27.1). Seed corpora are in each `fuzz_test.go`;
the one crash input is kept under `internal/sshd/testdata/fuzz/`.

| Target | Package | Execs (20 s) | Result |
|--------|---------|--------------|--------|
| FuzzParseRequestLine | gemini | 469 827 | Clean. Strict mode reproduces SR-08 on the seeds `%00` and `%0d%0a` (decoded control characters in `u.Path`). |
| FuzzSanitizeMeta | gemini | 46 375 | clean |
| FuzzPage | gemini | 674 091 | clean: `Text` never starts a line type, `Pre` always exactly two fences, one-line `Link`/`Heading`/`Item`. |
| FuzzPktReader | hooks | 84 663 | clean |
| FuzzProcReceive | hooks | 989 606 | clean: output is always a well-formed pkt stream, never `ok` without a daemon. |
| FuzzParseCommand | sshd | 301 390 | Clean. Strict mode reproduces SR-21 (`git-upload-pack 'alice/repo.o..000000'` accepted, kept in `testdata/fuzz/`). |
| FuzzCheckPath | vcs/git | 2 045 963 | Clean. Strict mode reproduces SR-08 (`"a\nb"` accepted). |
| FuzzCheckRefName | vcs/git | 1 652 001 | clean |
| FuzzMarkdownToGemtext | web | 69 858 | Clean. Strict mode reproduces SR-13 (`"=>  x"` from `[x](#top)`) and SR-12 (seed with an indented ``` line). |
| FuzzUserGemtext | web | 303 657 | clean: fences balanced, every link line marked, headings demoted. |
| FuzzRenderAnchored | web | 285 056 | Clean. Strict mode reproduces SR-06: `=> /~o/r/changes/7/v1/diff/../../../../account/keys/add?ssh-ed25519%20AAAA ...`. |
| FuzzRebaseGemtextLinks | web | 309 035 | clean |
| FuzzSplitTitleBody | forge | 20 s + 15 s | Clean after relaxing two over-strict checks: `"\n\r#000"` keeps a leading `#` and `"0\r0"` keeps a lone CR (SR-25, Info; both inputs kept in `testdata/fuzz/`). |
| FuzzParsePushOptions | forge | 386 301 | Control characters / invalid UTF-8 accepted in `title=` (SR-09), reported via `t.Skip` so the corpus stays green. |

No panics, hangs or memory blow-ups in any target (about 7.4 million
executions in total). All findings are invariant violations, not crashes.
Suggested CI addition: run every target with `-fuzztime=30s` nightly and the
seed corpora (`go test -run='^Fuzz'`) on every PR.

## 5. Prioritised patch list

Ordered by risk reduction per line changed. Snippets are illustrative; the
lead applies them.

### P-1 (SR-01) Bind Gemini INPUT actions to a per-user path token

Add to `internal/web/handler.go`:

```go
// actionToken derives an unguessable, per-user, per-action path segment.
// State-changing Gemini routes require it; a link planted in user content
// cannot know another user's token, so a query it carries is never acted on.
func (h *Handler) actionToken(u *store.User, action string) string {
	mac := hmac.New(sha256.New, h.actionSecret())
	mac.Write([]byte(strconv.FormatInt(u.ID, 10) + "\x00" + action))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:22]
}

// actionSecret is the cluster secret when clustered (so every node derives
// the same tokens) or a 32-byte value generated once into the settings
// table ("action_secret").
func (h *Handler) actionSecret() []byte { /* load once, cache in Handler */ }

// guardINPUT is the first thing a state-changing Gemini handler calls. With
// a valid token it returns true. Otherwise it redirects to the tokenised
// path, which drops any query a link may have carried, so the client will
// show the INPUT prompt and the user has to type the value.
func (h *Handler) guardINPUT(req *request, u *store.User, base, action, tok string) bool {
	want := h.actionToken(u, action)
	if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1 {
		return true
	}
	_ = gemini.Redirect(req.w, base+"/"+want)
	return false
}
```

Routes to convert (path gains a trailing `/<token>` segment; pages link to
`base + "/" + h.actionToken(u, action)`): `/new`, `/account/profile`,
`/account/keys/add`, `/account/keys/remove/<fp>`, `/account/certs/revoke/<spki>`,
`/account/certs/enrol-code`, `/account/enrol` (token derived from the SPKI
since there is no user yet), `/~o/r/settings/{description,visibility,archive,branch,delete}`,
`/~o/r/settings/collaborators/{add,remove}`, `/~o/r/issues/<n>/{close,reopen}`,
`/~o/r/issues/<n>/comments/<id>/delete`, `/~o/r/releases/<tag>/delete`,
`/~o/r/releases/<tag>/assets/<name>/delete`. Example for the SSH key route
in `account.go:161`:

```go
if len(rest) >= 1 && rest[0] == "add" {
	tok := ""
	if len(rest) == 2 {
		tok = rest[1]
	}
	if !h.guardINPUT(req, u, "/account/keys/add", "keys.add", tok) {
		return
	}
	q := req.Query()
	...
```

Event paths and feeds must keep pointing at the untokenised pages. Update the
threat model amendment: "INPUT-typed confirmation" becomes "INPUT on a
tokenised action path". Consider additionally making `keys/add` Titan-only.

### P-2 (SR-06) Validate and escape review anchor paths

`internal/web/changes.go`, replace the body of the anchor branch:

```go
path, line, ver := m[1], m[2], defaultVersion
clean, ok := anchorPath(path)
if !ok {
	plain = append(plain, l) // render as text, never as a link
	continue
}
...
out.WriteString(fmt.Sprintf("=> %s/v%d/diff/%s %s (v%d)\n", changeHref(rc, ch), ver, escapeSegments(clean), label, ver))
```

```go
func anchorPath(p string) (string, bool) {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	for _, r := range p {
		if r < 0x21 || r == 0x7f || strings.ContainsRune("?#%\\", r) {
			return "", false
		}
	}
	return p, true
}

func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return strings.Join(segs, "/")
}
```

Then set `FORGE_FUZZ_STRICT=1` for `FuzzRenderAnchored`.

### P-3 (SR-05, SR-09, SR-17) Validate change text from commits and push options

`internal/forge/changes.go` after line 168:

```go
title = oneLineTitle(title)                     // first line, trimmed, <= MaxTitleLen runes
body = truncateRunes(body, f.Config.Limits.MaxTextBytes)
if !utf8.ValidString(title) || !utf8.ValidString(body) || strings.ContainsRune(title+body, 0) {
	return fail(up.Ref, "commit message is not valid UTF-8 text; set a title with -o title=")
}
```

`parsePushOptions`, case `"title"`:

```go
v = strings.TrimSpace(v)
if v == "" || utf8.RuneCountInString(v) > MaxTitleLen || !utf8.ValidString(v) || hasControl(v) {
	return po, fmt.Errorf("title option is empty, too long or contains control characters")
}
```

`checkText`: add `|| strings.ContainsRune(title, 0)`. Replace `title[:MaxTitleLen]`
with a rune-boundary cut. Remove the `t.Skip` in `FuzzParsePushOptions`.

### P-4 (SR-08) Reject decoded control characters

`pkg/gemini/request.go`, after `u.Path` is final for both schemes:

```go
for _, c := range u.Path {
	if c < 0x20 || c == 0x7f {
		return nil, nil, ErrBadRequest
	}
}
```

`internal/vcs/git/runner.go` `checkPath`: same loop returning `vcs.ErrBadPath`.
Then set `FORGE_FUZZ_STRICT=1` for `FuzzParseRequestLine` and `FuzzCheckPath`.

### P-5 (SR-12, SR-13) Markdown fence and empty-target fixes

`internal/web/markdown.go:100-102`:

```go
code := strings.TrimPrefix(strings.TrimPrefix(line, "    "), "\t")
if strings.HasPrefix(code, "```") {
	code = " " + code
}
out.WriteString("```\n" + code + "\n```\n")
```

and in `flush`:

```go
for _, l := range links {
	if l[1] == "" {
		out.WriteString(gemini.EscapeLine(l[0]) + "\n")
		continue
	}
	out.WriteString("=> " + l[1] + " " + l[0] + "\n")
}
```

Then set `FORGE_FUZZ_STRICT=1` for `FuzzMarkdownToGemtext`. (Also: the
in-fence `if strings.HasPrefix(line, "```")` at `markdown.go:68` is dead code,
because the outer check at line 57 closes the fence first.)

### P-6 (SR-11, SR-16) Archived checks and comment deletion

Add `if acc.Repo.Archived { return ErrArchived }` to `EditIssue`,
`SetIssueState` (when `!closed`), `EditComment`, `DeleteComment`, `EditChange`,
`EditRelease`, `DeleteRelease`, `AddAsset`, `RemoveAsset`. In `EditComment`/
`DeleteComment` use `!acc.CanAdmin()` instead of `!acc.CanWrite()`.

### P-7 (SR-04) Reader pushes must not lock out the owner

`internal/forge/hooks.go:66-74`: apply the `MaxRepoBytes`/`MaxUserBytes`
checks only when `writer`; for readers keep `MaxChangeBytes` per push and add
`Limits.MaxChangeBytesPerRepo` (default 512 MiB) compared against the sum of
reader-pushed bytes, which requires a `pushed_bytes` column on
`change_versions` (recorded from `req.PushedBytes` in `procReceive`). Add
`forge admin change purge OWNER/NAME N` (delete `refs/changes/N/*`, mark the
row purged) and implement the owner block list ADR 0012 promises.

### P-8 (SR-15) Count commits with rev-list

```go
func (f *Forge) countCommits(ctx context.Context, repo vcs.Repository, old, new string) int {
	if old == "" {
		old = new + "^{}" // or CountCommits(ctx, "", new) variant: rev-list --count new
	}
	n, err := repo.CountCommits(ctx, vcs.RevisionID(old), vcs.RevisionID(new))
	if err != nil {
		return 0
	}
	return n
}
```

(`CountCommits` with an empty base needs a small `vcs` extension: `rev-list --count <new>`.)

### P-9 (SR-10) Per-IP limiter and 128-bit enrolment codes

Reuse `rateLimiter` keyed by `ipOf(req.RemoteAddr)` for `/account` (register,
enrol, enrol-code, keys/add) and `/new`, e.g. 10/min; change `[8]byte` to
`[16]byte` in `identity.go:156` and `account.go:223`.

### P-10 (SR-18) Touch SSH keys after authentication

Remove `TouchSSHKey` from `AuthenticateKey`; call it from `Authorize` (which
runs once per exec on an authenticated connection), or export the key id in
`Permissions.Extensions` and touch it in `handleConn`.

### P-11 (SR-02) Runtime secrets in tmpfs

Deploy ships `secrets.tar.age` to `/etc/forge/secrets.tar.age` (root 0600)
and a `forge-secrets.service` (root, oneshot, `Before=forge.service`,
`RequiredBy=forge.service`) runs
`age -d -i /etc/forge/age.key /etc/forge/secrets.tar.age | tar -x -C /run/forge/secrets`
into a 0750 root:forge directory under `RuntimeDirectory=forge`. `forge.toml`
points `cert_file`/`key_file`/`host_key_file`/`secret_file` at `/run/forge/secrets`.
`wg0.conf` and `bird.conf` keep their keys on disk unless `wg setconf` /
`bird -c` are fed from tmpfs too; document whichever is chosen and amend I-18.

### P-12 (SR-03) Bind peer name to peer address

`internal/repl/auth.go` `authenticate`: after the peer lookup,

```go
host, _, _ := net.SplitHostPort(r.RemoteAddr)
want, _, _ := net.SplitHostPort(n.opts.Peers[peer])
if host == "" || want == "" || net.ParseIP(host) == nil || !net.ParseIP(host).Equal(net.ParseIP(want)) {
	return "", fmt.Errorf("%w: %q from %s", ErrUnknownPeer, peer, r.RemoteAddr)
}
```

Log every forwarded write with `peer`, `path`, cert SPKI and status; add the
residual ("a compromised replica can replay any user's certificate") to
threat model section 8.

### Smaller items

- SR-20: drop `protocol.file.allow=always` (or scope to `Restore`), add
  `uploadpackfilter.sparse:oid.allow=false` and `uploadpackfilter.tree.maxDepth=2`,
  add `+refs/changes/*:refs/changes/*` to both fetch refspecs.
- SR-21: `strings.Contains(repo, "..")` in `ParseCommand`.
- SR-22/23/24: see table.
- SR-19: sudoers command list, pinned host keys, `ReadWritePaths=/var/lib/forge/tmp`
  on `forge-backup.service`.
- Required tests still missing (section 3) — the push corpus (test 3) and
  the Titan matrix (test 7) are the most valuable next additions.

## 9. Re-review 2026-09-13 (M10, surfaces added since 2026-09-10)

Scope: git push forwarding (`internal/repl/push.go`, `internal/sshd/forward.go`),
the documentation site (`internal/web/docs.go`), the fleet status page,
incidents and alerts feeds (`internal/web/status.go`, `alerts.go`,
`internal/repl/status.go`), the stats loop and `forge admin release`
(`cmd/forge`), the monitoring host (`infra/opentofu/modules/{vultr-monitor,
monitor-node}`, `infra/cloud-init/monitor.yaml.tftpl`, `scripts/deploy
monitor`), the mesh changes (`scripts/netgen`, `infra/firewall`), and the
off-site backup path (`infra/backup/forge-offsite`, `forge-deploy-helper`
`forge-backup.env`, `infra/backup/b2.yaml`, `scripts/b2check`). Method as in section 0,
plus a read of the live nodes' units and firewall.

| ID | Severity | Where | Finding | Disposition |
| --- | --- | --- | --- | --- |
| SR-26 | Medium | web (alerts feed) | `/status/alerts/<id>` printed every Prometheus label and annotation of an alert, including `instance` (a WireGuard mesh address and port) and descriptions that interpolate them: the control network's addressing was public. | **Fixed**: an allowlist of labels (`alertname`, `pop`, `node`, `severity`, probe labels) and redaction of IPv6 literals in annotation text (`publicText`); test asserts no `fda5:` / `instance =` on the page. |
| SR-27 | Low | status page | `/status/` publishes per-POP version strings, uptimes and replication lag. Version disclosure eases targeting of a known-vulnerable build; the rest is operational transparency the page exists for. | **Accepted**: the version is also in the release page and the repository; the status page is the operator's own transparency choice. Revisit if a build ever ships with a known unfixed vulnerability (rotate the version string or hide it). |
| SR-28 | Low | push forwarding | The leader trusts the replica's assertion of the pushing user (account id, name, fingerprint) on `/v1/forward/receive-pack`, authenticated only by the shared cluster secret. A compromised replica can push as any user whose key it has accepted. | **Accepted, documented** (`docs/replication.md`): identical to the forwarded-Titan-writes model (T-37); the leader re-runs every other check. Same-secret peers are already fully trusted for metadata replication. |
| SR-29 | Info | push forwarding | The hijacked control-plane connection reads stdin from the raw socket, not `bufrw.Reader` (net/http would cancel the request context on the replica's half-close). Frames are bounded (`maxFrame` 1 MiB) and `receive-pack` runs under `Config.SessionTimeout` and `receive.maxInputSize`; a slow or stalled replica holds a leader goroutine and one git slot until the session timeout. | **Accepted**: bounded by the same limits as a direct SSH push; git slots are the existing back-pressure. |
| SR-30 | Info | docs site | `/docs/` reads a configured public repository only (anonymous `LookupRepo`, so a private repository is never published whatever certificate is presented); dot segments are normalised and anything escaping the tree is 404; git rejects invalid paths (`ErrBadPath`). Non-text blobs are served with a MIME type by extension. | No change. Note: the site serves whatever is pushed to the configured repository; only writers of that repository can change it. |
| SR-31 | Info | control plane | `/v1/status` now carries the health snapshot and every node polls every peer every 30 s; the fleet view is served publicly by `/status/`. All of it is authenticated by the cluster secret on the mesh; the public page shows only derived fields (state, verdict, lag, version, uptime), never addresses. | No change. |
| SR-32 | Low | monitoring host | mon1 exposes only 2200 (OpenSSH, rate-limited, keys only), 51820 (WireGuard) and ICMP; Prometheus binds `[::]:9090` and Grafana `127.0.0.1:3000`, with nftables admitting 9090/3000 from `lo` and `wg0` only. Its one secret (WireGuard key) is plaintext in `/etc/wireguard/wg0.conf` 0600 root, unlike the POPs' tmpfs bundle. Its host key was pinned by trust-on-first-use (no console read). | **Accepted**: blast radius of the key is read access to the POPs' metrics ports over the mesh. Operator action: compare the pinned fingerprint with the Vultr console once (`docs/status.md`). |
| SR-33 | Info | mesh | The POPs' `/48` unicast addresses cannot be reached through the anycast catchment: the catching POP's forward chain drops and the receiving POP's WireGuard rejects third-party sources. This is a property, not a hole: the mesh carries only mesh-sourced traffic. The design text that assumed backhaul was corrected and the probes removed. | No change. |
| SR-34 | Low | off-site backups | The operator holds no account-wide B2 key at all (bucket and keys are made in the console, `infra/backup/b2.yaml`, verified by `scripts/b2check`). Nodes hold a B2 application key restricted to one bucket with `listBuckets, listFiles, writeFiles`: a compromised node can write (and thus create new versions of) archives and list names, but cannot read other nodes' archives (no `readFiles`) or delete anything (no `deleteFiles`); retention is the bucket lifecycle. Archives are age-encrypted before upload, so B2 never sees plaintext. `/etc/default/forge-backup` is validated by the helper to the three `OFFSITE_*` keys with `forge-offsite` as the only command. | No change. Note `listFiles` lets a node see other nodes' archive names (stamps only). |
| SR-35 | Info | admin CLI | `forge admin release create|asset --as USER` runs with the acting user's permissions through the same domain checks as Titan; it is root-only in practice (`sudo -u forge` on the node) and writes an event naming the user. | No change. |

| SR-36 | Medium | web (action tokens) | SR-01's tokens were served at `/_/<token>/<path>`, outside the URL prefix a client scopes its identity to, so the token path was requested without the certificate the token is bound to. Verification could never succeed: the server redirected to the plain path, which issued a new token. Any query-driven write was unreachable from such a client; observed as a redirect loop enrolling a certificate from an identity scoped to `/account`. | **Fixed** (v0.1.2): the token is a suffix of the resource's path (`<path>/_/<token>`), so it stays inside any scope that covers the resource. Regression test `TestActionTokenScope`. |

Residual-risk sign-off (internal): the invariants of section 6 still hold
on `main` at this date; SR-24 and SR-27/28/29/32/34 are accepted with the
notes above. The external review of the SSH exec handler, Titan parser,
pre-receive scanner and authorisation (M10, first checkbox) remains open
and is the operator's to commission.
