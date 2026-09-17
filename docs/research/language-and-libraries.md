# Language and library evaluation: Go vs Rust for the Gemini-native forge

Status: research, 2026-09-10. Scope: a single static binary that runs a Gemini
server (read), a Titan server (write), a restricted Git-over-SSH server
(clone/fetch/push, public-key auth), uses TLS client certificates as identity,
and keeps metadata in SQLite. Target host: a 1–2 GB VPS.

All version numbers and dates below were checked on 2026-09-10 against
pkg.go.dev, crates.io, or the project's GitHub/sourcehut API.

## 0. Recommendation (short)

**Go.** Go 1.27 (released 2026-08) with `crypto/tls`, `golang.org/x/crypto/ssh`,
`modernc.org/sqlite` and a hand-written ~500-line Gemini/Titan server. Shell
out to the system `git` for transport (`git-upload-pack` / `git-receive-pack`)
and for browsing plumbing, exactly as soft-serve, Gitea, gogs and sourcehut do.

Why Go and not Rust, for this project specifically:

1. Everything on the hot security path (TLS with client certs, SSH, X.509
   parsing, SHA-256 fingerprints) is in Go's standard library or `x/crypto`,
   which get coordinated security releases (eight `x/crypto/ssh` CVEs were
   fixed May–Sep 2026, all within days of disclosure). In Rust the same
   surface is rustls + russh + x509-parser + a crypto provider, i.e. four
   independently maintained third-party trees.
2. The SSH-session-to-subprocess plumbing (pipe an SSH channel into
   `git-receive-pack`) is a goroutine and two `io.Copy`s in Go. In russh it is
   a callback-driven `Handler` (`data()`, `channel_eof()`, window accounting)
   that you glue to a `tokio::process::Child` yourself.
3. `CGO_ENABLED=0 go build` gives a static, cross-compiled binary with zero
   toolchain setup; the Rust equivalent (musl target + `cc` for bundled SQLite
   + several minutes of clean build) is fine but is a real recurring cost.
4. The four closest prior-art projects (soft-serve, Gitea, gogs, git.sr.ht's
   shell) are all Go; their code is directly reusable as reference.

Rust would win on idle memory (roughly 3–8 MB vs 10–20 MB RSS) and on binary
size (5–10 MB vs 15–25 MB), neither of which matters at 1–2 GB, and it would
win if we wanted in-process Git object access at scale (gitoxide is now
clearly ahead of go-git). It loses on time-to-first-working-server and on
supply-chain surface. A scoring table is in section 13.

## 1. Gemini server libraries

### Go

| Library | Latest | Health | Notes |
|---|---|---|---|
| `git.sr.ht/~adnano/go-gemini` | v0.2.6, 2024-05-11 ([versions](https://pkg.go.dev/git.sr.ht/~adnano/go-gemini?tab=versions)) | Quiet but complete; 53 importers; targets spec v0.16.0 | `net/http`-style `Handler`/`ResponseWriter`; `Server{Addr, Handler, ReadTimeout, WriteTimeout, GetCertificate}`; client cert exposed as `Request.TLS()` (`*tls.ConnectionState`) and SNI via `Request.ServerName()`; status constants for 10–62 ([docs](https://pkg.go.dev/git.sr.ht/~adnano/go-gemini)). |
| `github.com/kulak/gemini` | last push 2024-10-17, 9 stars ([API](https://api.github.com/repos/kulak/gemini)) | Hobby | Only Go lib that also does **Titan** and client certs; README admits request reading "seems to be inefficient". Reference only. |
| `github.com/ninedraft/gemax` | last push 2026-04-09, 8 stars | Hobby | Small. |
| `github.com/a-h/gemini` | last push 2025-04-21, 53 stars | Idle | Server+client, no Titan. |
| `github.com/makew0rld/go-gemini` | client-focused | — | Used by Amfora; not a server. |
| `github.com/LukeEmmet/molly-brown` | server binary, not a lib | — | Worth reading: "certificate zones" gate paths by SHA-256 fingerprint, analogous to `authorized_keys` ([pkg](https://pkg.go.dev/github.com/LukeEmmet/molly-brown)). |

The Gemini spec has been frozen for years, so go-gemini's 2024 date is not
alarming, but it has one maintainer and its `Server` owns the TLS listener
loop, which makes it awkward to share a listener/port with Titan and to apply
our own per-connection deadlines and connection caps.

### Rust

| Crate | Latest | Health | Notes |
|---|---|---|---|
| `windmark` | 0.7.0, 2026-05-29; repo pushed 2026-06-13, 15 stars ([crates](https://crates.io/api/v1/crates/windmark), [repo](https://github.com/gemrest/windmark)) | Maintained, tiny userbase | Built on **OpenSSL** (`openssl 0.10`, `tokio-openssl`), not rustls ([Cargo.toml](https://raw.githubusercontent.com/gemrest/windmark/main/Cargo.toml)). Exposes `RouteContext.certificate: Option<X509>`. OpenSSL dependency defeats the pure-Rust static-binary goal. |
| `twinstar` (ex-`northstar`) | 0.4.0, 2022-05-02 | Dead | 1.9k downloads total. |
| `titanite` | 0.3.2, 2025-02-20; 4.6k downloads ([crates](https://crates.io/api/v1/crates/titanite)) | Hobby | Gemini + Titan client/server lib from YGGverse. |
| `tokio-gemini`, `gmi` | — | — | Not server frameworks (gmi is a client/gemtext crate). |
| `agate` (server binary) | active | Good reference | rustls + tokio, SNI via `with_cert_resolver`, `--only-tls13`, request parsed into a 1026-byte stack buffer; but it uses `.with_no_client_auth()`, i.e. **no client-cert support** to copy ([main.rs](https://raw.githubusercontent.com/mbrubeck/agate/master/src/main.rs)). |

### Verdict

Write our own. The protocol is: accept TLS, read ≤1024 bytes + CRLF, parse a
URL, write `NN meta\r\n` + body, `close_notify`. Status codes 10/11, 20,
30/31, 40–44, 50–53, 60/61/62 ([spec](https://geminiprotocol.net/docs/protocol-specification.gmi)).
A hand-written Go server that also handles Titan, per-host SNI, client-cert
fingerprinting, connection deadlines and a concurrency cap is ~500 lines and
has no third-party dependency. Use go-gemini's `server.go` as a checklist for
edge cases (request size, userinfo/fragment rejection, close_notify). In Rust
the same is ~600–800 lines on tokio-rustls, plus the custom verifier in
section 3.

## 2. Titan

There is no library worth depending on in either language. The spec
([transjovian.org](https://portal.mozz.us/gemini/transjovian.org/titan/The%2520Titan%2520Specification))
is small:

- URL: `titan://host/path;size=N;mime=type;token=str` — parameters are
  semicolon-delimited, appended to the path, before any query. `size` is
  mandatory ("the number of bytes you are going to upload"); `mime` defaults
  to `text/gemini`; `token` is optional and unused when client certificates
  carry identity.
- Request line + CRLF, then exactly `size` bytes.
- Server may reject before reading the body (and close), or after: `20`,
  `30 gemini://…` (redirect to the resulting page), `5x` on error.
- `size=0` is the normative delete.
- GmCapsule's defaults are a good policy baseline: client certificate required
  for uploads (`require_identity = true`), 10 MiB `upload_limit`, and the
  handler runs only after the whole body has been received
  ([manual](https://geminispace.org/gmcapsule/gmcapsule.html)).

Implementation on top of our Gemini server: detect scheme `titan`, parse the
`;k=v` path params, require a client cert (else `60`), enforce a hard size cap
before reading (else `59`/`50`), `io.LimitReader`/`io.ReadFull` the body into a
temp file, hand it to the write handler. Estimate: 150–250 lines Go, same in
Rust. The interesting work is the *semantics* (what a Titan PUT means for a
forge: issue creation, comment, wiki page, or a `git bundle` upload), not the
protocol.

## 3. TLS client certificates

### Go `crypto/tls` (Go 1.27)

Configuration for Gemini identity:

```go
&tls.Config{
    MinVersion:     tls.VersionTLS12,      // spec minimum; consider 1.3-only per host
    ClientAuth:     tls.RequestClientCert, // ask, don't require; ClientCAs stays nil
    GetCertificate: certByServerName,      // SNI / multiple hostnames
    // GetConfigForClient can vary ClientAuth per SNI host if some vhosts never need certs.
}
```

- With `RequestClientCert`, Go sends `CertificateRequest`, does **no chain
  verification**, and leaves `verifiedChains == nil` in `VerifyPeerCertificate`
  ([docs](https://pkg.go.dev/crypto/tls)). After `Handshake()`,
  `ConnectionState().PeerCertificates[0].Raw` is the DER; identity is
  `sha256.Sum256(raw)`. That is the whole "accept any self-signed cert" story.
- Go parses every client certificate with `x509.ParseCertificate` *during the
  handshake* and aborts with `tls: failed to parse client certificate` if it
  fails; it also rejects unsupported key types (accepts ECDSA, RSA, Ed25519,
  and ML-DSA on TLS 1.3) ([handshake_server.go](https://go.dev/src/crypto/tls/handshake_server.go)).
  Consequence: a malformed cert from an odd generator fails the TLS handshake
  and we never get to answer `62`. Known strictness: since Go 1.23,
  `ParseCertificate` rejects **negative serial numbers** (`x509negativeserial`
  GODEBUG, [godebug](https://go.dev/doc/godebug)); SHA-1 signatures are only
  rejected in `Verify`, which we don't call. We can set
  `//go:debug x509negativeserial=0` if real clients hit it.
- TLS 1.3 post-handshake client auth is not supported and is on
  "Proposal-Hold" ([#40521](https://github.com/golang/go/issues/40521)). This
  does not matter for Gemini: the request is sent only after the handshake and
  the server does not know the path before then, so the cert must be
  requested on every connection anyway, which `RequestClientCert` does.
- Session resumption: Go stores the client cert inside TLS 1.3 session
  tickets so resumed connections still populate `PeerCertificates`
  ([#77379](https://github.com/golang/go/issues/77379) complains the tickets
  are big; it's open with no milestone). For Gemini, where every request is a
  new connection, resumption is a win; keep tickets enabled (Go rotates keys
  automatically).
- `close_notify`: `tls.Conn.CloseWrite()` then `Close()`.
- Expiry policy: Go doesn't check validity when not verifying; many Gemini
  clients use very long-lived or already-expired certs. Recommendation:
  identity = fingerprint; expiry is ignored for lookup but reported as `62`
  only when *registering* a new cert.
- The known TLS 1.3 gotcha ([#56371](https://github.com/golang/go/issues/56371))
  is client-side (a Go *client* only sees a bad-certificate alert on first
  read); irrelevant to a server.
- Multiple hostnames: `GetCertificate` keyed on `hello.ServerName`. Gemini
  clients are TOFU, so prefer long-lived self-signed certs per host over ACME
  (90-day rotation triggers TOFU warnings in every client). `x/crypto/acme/autocert`
  is available in the same module if we ever want CA certs.

### Rust rustls 0.23.44 (2026-09-07)

- `WebPkiClientVerifier` needs trust anchors and cannot accept arbitrary
  self-signed certs. You must implement
  `rustls::server::danger::ClientCertVerifier` with five required methods:
  `root_hint_subjects()` (return `&[]`), `verify_client_cert()` (return
  `Ok(ClientCertVerified::assertion())` after any policy check),
  `verify_tls12_signature()`, `verify_tls13_signature()` (delegate to
  `rustls::crypto::verify_tls12_signature` / `verify_tls13_signature` with the
  provider's `signature_verification_algorithms`), and
  `supported_verify_schemes()`; override `client_auth_mandatory()` to `false`
  ([trait docs](https://docs.rs/rustls/latest/rustls/server/danger/trait.ClientCertVerifier.html)).
  Install with `ServerConfig::builder().with_client_cert_verifier(Arc::new(v))`.
  Roughly 60–80 lines; the module is literally named `danger`, and the
  signature-verification methods are where a mistake silently breaks auth
  (the docs warn rustls "does not enforce certain cryptographic requirements
  for you"). Fingerprint the DER from `ServerConnection::peer_certificates()`.
  Parse with `x509-parser` 0.18.1 (2026-02) only if you need expiry/subject.
- SNI: `with_cert_resolver(Arc<dyn ResolvesServerCert>)` (agate does this).
- Upside vs Go: rustls hands you the raw DER without parsing it, so a
  malformed-but-usable cert can be accepted or turned into a `62` at the
  application layer instead of a handshake failure.

Verdict: both work; Go is ~20 lines of config, Rust is a small unsafe-feeling
trait impl that we would own forever.

## 4. SSH server

### Go

- `golang.org/x/crypto/ssh` **v0.57.0, 2026-09-08** ([versions](https://pkg.go.dev/golang.org/x/crypto/ssh?tab=versions)).
  Key algorithms: `ssh-ed25519`, `ecdsa-sha2-nistp{256,384,521}`,
  `rsa-sha2-256/512` (and legacy `ssh-rsa`), FIDO
  `sk-ssh-ed25519@openssh.com` / `sk-ecdsa-sha2-nistp256@openssh.com`, and all
  `*-cert-v01@openssh.com` certificate forms ([constants](https://pkg.go.dev/golang.org/x/crypto/ssh#pkg-constants)).
  `PublicKeyCallback` may be invoked more than once per key and must not be
  used as the sole source of truth for which key authenticated (lesson of
  CVE-2024-45337; compare the fingerprint in `Permissions.Extensions`
  against `conn.Permissions` after `NewServerConn`, which is what
  soft-serve's `AuthenticationMiddleware` does).
  Security track record 2026: CVE-2026-39827/39830/39832/39835 (May),
  CVE-2026-42508 and CVE-2026-56854 (source-address restriction bypass in
  non-publickey callbacks), CVE-2026-56855 and CVE-2026-78662 (channel
  deadlocks, 2026-09-02) — all DoS/authz-hardening class, all fixed in
  point releases ([golang-announce](https://groups.google.com/g/golang-announce/c/1y3fb2np35U),
  [CVE-2026-56855](https://radar.offseq.com/threat/cve-2026-56855-cwe-770-allocation-of-resources-without-limits-or-throttling-in-golangorgxcrypto-08d28ea7c4199d87)).
  NCC Group did a public cryptographic review of `x/crypto/ssh`
  ([report](https://www.nccgroup.com/research/public-report-go-xcryptossh-cryptographic-implementation-review/)).
  That cadence is a feature: govulncheck flags them and a `go get -u` fixes
  them.
- `github.com/gliderlabs/ssh` last release **v0.3.8, 2024-12-13** (only a
  dependency bump for CVE-2024-45337); issue backlog growing. Treat as frozen.
- `charm.land/ssh` (was `github.com/charmbracelet/ssh`) **v0.4.3,
  2026-08-07** — Charm's maintained fork of gliderlabs, used by Wish and
  soft-serve; the 0.4.3 release fixed a deadline data race between
  `HandshakeTimeout` and `IdleTimeout`.
- Gitea (`modules/ssh/ssh.go`) and gogs (`internal/ssh/ssh.go`) both use
  `x/crypto/ssh` directly: accept only `session` channels, only `exec`
  requests, then run `gitea serv key-<id>` / `gogs serv` with
  `SSH_ORIGINAL_COMMAND` in the env and pipe the channel to the child.

Recommendation: use `x/crypto/ssh` directly, no framework. A git-only server
wants *fewer* features than gliderlabs offers (no PTY, no shell, no
subsystems, no forwarding, no agent). ~300–400 lines: listener, `ServerConfig`
with `PublicKeyCallback` (lookup by `ssh.FingerprintSHA256`), reject every
channel type except `session`, reject every request except `exec` (and
`env` for `GIT_PROTOCOL=version=2` only), parse the command, authorize,
spawn git, `io.Copy` both ways, send `exit-status`. Host keys: generate
Ed25519 on first start (plus RSA-4096 for old clients if wanted), store under
the data dir, log the fingerprints.

### Rust

- `russh` **0.63.3, 2026-09-09**, 6.75M downloads, 1.9k stars, Apache-2.0,
  pushed daily ([crates](https://crates.io/api/v1/crates/russh),
  [repo](https://api.github.com/repos/Eugeny/russh)). Fork of thrussh
  (which is archived). Host/user keys: Ed25519, RSA (rsa-sha2-256/512),
  ECDSA P-256/384/521, OpenSSH certificates; kex includes
  `mlkem768x25519`, curve25519, ECDH, DH-GEX; ciphers chacha20-poly1305,
  AES-GCM/CTR. Server: exec, pty, subsystem, local/remote/unix forwarding,
  agent forwarding ([README](https://raw.githubusercontent.com/Eugeny/russh/main/README.md)).
  **FIDO `sk-*` keys are not listed** in the README; verify signature
  support before relying on it. Needs a crypto backend (aws-lc-rs or ring).
- Handler is a native-`async` trait: `auth_publickey(user, &PublicKey) ->
  Auth`, `exec_request(channel, data, session)`, `subsystem_request`,
  `data`, `channel_eof`, `channel_close`; `Config{keys, auth_rejection_time,
  inactivity_timeout, methods}`; `run_on_address`
  ([Handler](https://docs.rs/russh/latest/russh/server/trait.Handler.html)).
  Piping to a subprocess means owning a per-channel task and forwarding
  `data()` into the child's stdin with backpressure. Doable, more code.
- CVEs: CVE-2023-28113 (DH validation), CVE-2023-48795 (Terrapin),
  CVE-2024-43410 (unbounded allocation), CVE-2025-54804 (window-adjust
  overflow), plus a 2025 pty-req DoS fixed in 0.62.4; none in 2026 so far
  ([stack.watch](https://stack.watch/product/russhproject/russh/)). Fewer
  eyes than `x/crypto/ssh`, which is a mixed signal.

## 5. Git integration

Every serious Go/Python forge shells out for transport, and so should we:

- **soft-serve** (`pkg/git/service.go`): `exec.CommandContext(ctx, "git",
  "-c", "uploadpack.allowFilter=true", "upload-pack", …)`, `StdinPipe`/
  `StdoutPipe`, goroutines + `WaitGroup`; SSH commands dispatched through a
  Cobra root command (`git-upload-pack`, `git-receive-pack`,
  `git-upload-archive`, `git-lfs-transfer`, `repo`, `user`, …) after an
  auth middleware compares the session's key fingerprint with the one
  recorded at `PublicKeyHandler` time ([middleware.go](https://raw.githubusercontent.com/charmbracelet/soft-serve/main/pkg/ssh/middleware.go)).
- **Gitea** requires git ≥ 2.25, detects features by version
  (`proc-receive` ≥2.29, SHA-256 repos ≥2.42) and runs everything through
  the CLI ([modules/git/git.go](https://raw.githubusercontent.com/go-gitea/gitea/main/modules/git/git.go)).
- **git.sr.ht** `cmd/shell/main.go` (Go): whitelist
  `git-receive-pack|git-upload-pack|git-upload-archive`; canonicalize the
  path with `path.Join("/", last_arg)` then join onto the repos root;
  `git-receive-pack` needs WRITE, others READ; decide from
  owner/visibility/ACL rows in SQL; suspended users refused; then
  `syscall.Exec` into git with `SRHT_PUSH_CTX=<json>` in the environment so
  the update hook doesn't repeat the lookups. ~450 lines total. Deploy keys
  are limited to the one repo they were added to.
- **Gitolite** matches both `git-upload-pack` and `git upload-pack` spellings
  (git ≥2.14 may send either) and treats `receive-pack` as the only write
  ([docs](https://gitolite.com/gitolite/how.html)).

### Reading objects for the Gemini UI

| Option | Latest | Assessment |
|---|---|---|
| `git` plumbing via exec | git 2.53 (Feb 2026) | `cat-file --batch-command` (long-lived per-repo process), `ls-tree -l`, `log --format=%H%x00…`, `diff-tree -p -M --stat`, `blame --porcelain`, `for-each-ref`, `rev-list --count`. Zero deps, always correct (commit-graph, bitmaps, replace refs, mailmap, `.gitattributes`). Latency 2–5 ms/spawn; amortize with the batch process. |
| `github.com/go-git/go-git/v5` | v5.19.2, 2026-07-29 ([releases](https://api.github.com/repos/go-git/go-git/releases?per_page=8)) | Pure Go, 7.7k stars, 235 open issues. Fine for tree/blob/log on small–medium repos; no commit-graph/bitmap use, high memory on large packs ([classic issue](https://github.com/src-d/go-git/issues/447)). v6 is at alpha.5 (2026-07-29) with a new transport API and a git server implementation; not stable yet. |
| `github.com/gogs/git-module` | v1.8.9, 2026-07-17 | Thin CLI wrapper (soft-serve uses a fork). Little value over our own wrapper. |
| `github.com/libgit2/git2go` | last push 2024-03-04, 84 open issues | cgo + libgit2; kills `CGO_ENABLED=0`. No. |
| `gix` (gitoxide) | 0.87.1, 2026-08-24; 11.9k stars, pushed daily ([status](https://raw.githubusercontent.com/GitoxideLabs/gitoxide/main/crate-status.md)) | Done: object read/write, tree diff with rename tracking, blob line diff, rev-walk, blame (basic), fetch over file/ssh/git/http, pack reading/indexing. Not done: push, **server-side protocol**, bundle-uri, hooks, delta compression when writing packs. So even in Rust, transport is `git` CLI. Excellent for in-process browsing. |
| `git2` | 0.21.0, 2026-05-18 | libgit2 bindings; builds statically with musl but adds a C toolchain. |

Specific operations (all via CLI in both languages):

- Trees/blobs: `git cat-file --batch-command` (git ≥2.36) with `contents`,
  `info` and `flush`; `git ls-tree -l -z`.
- Log: `git log -z --format=… --no-merges? -n N <rev> -- <path>`.
- Diff: `git diff-tree -p -M -C --stat --root <a> <b>`; render unified diff
  as preformatted gemtext.
- Refs: `git for-each-ref --format=…` (or read `packed-refs`/loose refs via
  go-git later).
- Blame (optional): `git blame --porcelain -w`; slow on big files — cache.
- Bundles: `git bundle create <file> --all` for downloads and as a
  Titan-upload import format.
- Replication/backup: a bare mirror + `git fetch --prune origin
  +refs/*:refs/*` on a timer; restore = clone.
- Health: `git fsck --connectivity-only --no-dangling`, `git count-objects -v`,
  `git gc --auto` under a per-host semaphore (pack-objects is what will OOM a
  1 GB box, not our process; set `-c pack.threads=1 -c pack.windowMemory=64m`
  and cap concurrent transport processes).
- Hooks: install `pre-receive`, `update`, `post-receive` as tiny scripts (or
  point `-c core.hooksPath=<shared dir>` on our own `receive-pack` spawn) that
  exec `forge hook <name>` from the same binary. The daemon sets
  `FORGE_USER=…`, `FORGE_REPO=…`, `FORGE_PUSH_ID=…` in the child's env (the
  sourcehut `SRHT_PUSH_CTX` pattern); the hook talks back to the daemon over
  a Unix socket for authorization (`update`: per-ref rules, force-push and
  deletion policy) and event emission (`post-receive`: update SQLite,
  regenerate gemtext, notify). Also pass `-c receive.fsckObjects=true` and
  `-c receive.denyDeleteCurrent=true` when spawning receive-pack.

## 6. SQLite

| Option | Latest | Notes |
|---|---|---|
| `modernc.org/sqlite` | v1.59.0, 2026-09-05 (bundles SQLite **3.53.4**; CHANGELOG notes it includes upstream's journal-rollback corruption fix) ([changelog](https://gitlab.com/cznic/sqlite/-/raw/master/CHANGELOG.md), [pkg](https://pkg.go.dev/modernc.org/sqlite?tab=versions)) | cgo-free transpile. ~2x slower INSERT, 10–100 % slower SELECT than cgo ([benchmark](https://datastation.multiprocess.io/blog/2022-05-12-sqlite-in-go-with-and-without-cgo.html)). Irrelevant at forge-metadata volumes. Used by soft-serve (v1.56.0). |
| `github.com/mattn/go-sqlite3` | active | cgo; fastest; breaks `CGO_ENABLED=0` cross-builds. No. |
| `rusqlite` | 0.40.2, 2026-08-08 | `bundled` feature compiles SQLite C; works on musl via `cc`. Standard choice in Rust. |

Settings: open with
`file:forge.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)`,
one connection dedicated to writes (`SetMaxOpenConns(1)` on a write pool, a
separate read pool), `wal_autocheckpoint` default. Migrations:

- `github.com/pressly/goose/v3` **v3.28.0, 2026-09-02** — embedded `.sql`
  via `embed.FS`, supports the `sqlite` (modernc) driver, Provider API.
  Recommended.
- `github.com/golang-migrate/migrate/v4` v4.20.1, 2026-09-09 — fine, heavier.
- Hand-rolled `schema_version` + embedded SQL is ~50 lines and a legitimate
  choice for a single-binary tool.
- Rust: `refinery` 0.9.2 (2026-06-10) with rusqlite; `sqlx migrate` if using
  sqlx (we would not; async SQLite buys nothing here).

Backup/replication: **Litestream v0.5.17, 2026-08-31**
([release](https://api.github.com/repos/benbjohnson/litestream/releases/latest)) as a sidecar
replicating the WAL to S3-compatible storage; its VFS became writable in
Feb 2026 and ships as `litestream-vfs`. Litestream takes over WAL
checkpointing — the app must not force `wal_checkpoint(TRUNCATE)` itself.
Alternative: `VACUUM INTO` nightly + `git bundle`s of every repo.

## 7. Static binaries, cross-compilation, size, build time, memory

| | Go 1.27 | Rust 1.98 |
|---|---|---|
| Static build | `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'`; nothing else needed with modernc sqlite | `--target x86_64-unknown-linux-musl`; bundled SQLite needs a C compiler for the target (`cross`, `cargo-zigbuild`, or `musl-gcc`) |
| Cross-compile | `GOOS=linux GOARCH=arm64` env vars | per-target toolchain + linker setup |
| Binary size | 15–25 MB for a server with TLS+SSH+SQLite (`-s -w` → ~12–18 MB; UPX optional) | 5–10 MB with musl + LTO + `strip` |
| Clean build | seconds; ~30 modules | 2–5 min; 300–500 crates (tokio, rustls, russh, gix, rusqlite, clap, tracing) |
| Idle RSS | ~10–20 MB; +~8 KB stack per goroutine; TLS conn buffers ~ tens of KB | ~3–8 MB; tasks are hundreds of bytes; rustls per-conn buffers similar |
| Peak memory driver | `git pack-objects` children, in both languages | same |

(Comparative figures: [stanza.dev](https://www.stanza.dev/compare/go-vs-rust),
[rustify](https://rustify.rs/articles/rust-vs-go-2026); treat as order-of-magnitude.)
On a 1–2 GB VPS both are comfortable; what needs a budget is concurrent git
subprocesses (semaphore of 4–8) and per-connection deadlines so idle Gemini
connections cannot accumulate.

## 8. Concurrency model

Go: goroutine per connection with `SetDeadline`; SSH session = goroutine
pair copying channel↔subprocess; `context` cancels git on disconnect. Many
idle TLS/SSH connections cost ~10–50 KB each; 10k idle connections is
hundreds of MB — cap accepts with a semaphore and use short handshake/read
deadlines (Gemini requests should complete within seconds).

Rust/tokio: cheaper per connection and no GC pauses; russh's callback
`Handler` inverts control, so subprocess piping needs channels and explicit
window/backpressure handling. Fine, but more code and more places to get
cancellation wrong.

## 9. Observability

- Go: `log/slog` (stdlib, JSON handler), `github.com/prometheus/client_golang`
  **v1.24.1, 2026-07-24**, `net/http/pprof` and `expvar` on a
  localhost-only listener. Metrics worth having from day one: connections by
  protocol, handshake failures, status-code histogram, git subprocess
  duration/exit codes, auth failures by method, DB write latency.
- Rust: `tracing` 0.1.44 (2025-12-18) + `tracing-subscriber`;
  `prometheus-client` 0.25.1 (2026-09-01, the OpenMetrics-native one) rather
  than `prometheus` 0.14.0 (2025-03-27, slower cadence).

## 10. CLI frameworks

Go: `github.com/spf13/cobra` v1.10.2 (2025-12-03; used by soft-serve as the
SSH command dispatcher too), `github.com/urfave/cli/v3` v3.11.0 (2026-08-16),
or stdlib `flag` with a 40-line subcommand switch. For `forge serve|hook|admin
user add|repo create`, stdlib `flag` is enough and removes a dependency;
adopt cobra only if the admin surface grows. Rust: `clap` 4.6.6 with derive.

## 11. Supply-chain and security posture

- Go: `govulncheck` (golang.org/x/vuln **v1.8.0, 2026-09-08**) is
  call-graph based, so it reports only reachable vulnerabilities; module proxy
  + checksum DB by default; `go mod verify`. Proposed direct deps: 4
  (`x/crypto`, `modernc.org/sqlite`, `goose`, `client_golang`) → roughly 25–35
  modules in `go.sum`, most of them modernc's libc shims. All TLS/X.509/SSH
  code is first-party Go team code.
- Rust: `cargo-audit` 0.22.2 (2026-06-05), `cargo-deny` 0.20.2 (2026-07-09),
  `cargo-vet` for review attestations. The stack above pulls 300–500 crates.
  2026 registry incidents worth remembering: Cargo symlink extraction
  CVE-2026-5223 ([blog](https://blog.rust-lang.org/2026/05/25/cve-2026-5223/)),
  `tar` crate CVE-2026-33056, and the August 2026 build-time dropper in
  `arrayref`/`internment`/`append-only-vec` ([Hacker News coverage](https://thehackernews.com/2026/08/rust-supply-chain-attack-puts-build.html)).
  Build scripts and proc-macros execute at build time; Go has no equivalent.
- Either way: pin versions, vendor or use a lockfile, run the scanner in CI,
  build in a clean container, publish checksums and (Go) reproducible builds
  via `-trimpath`.

## 12. Prior art and what to steal

| Project | Stack | Take-away |
|---|---|---|
| soft-serve v0.12.2 (2026-08-07), 7.2k stars, MIT | Go; `charm.land/ssh` + `wish/v2`; git via `exec` (`-c uploadpack.allowFilter=true`); go-git v5.19.2 + git-module; modernc sqlite + sqlx; prometheus; cobra as SSH command router ([go.mod](https://raw.githubusercontent.com/charmbracelet/soft-serve/main/go.mod)) | Middleware order (context → auth → logging → commands); fingerprint re-check after auth; `PublicKeyHandler` accepts any key and defers the DB decision. Its Aug 2026 LFS path-traversal CVE (unvalidated object IDs reached the SSH host key and JWT signing key) is the exact bug class to design against: canonicalize every path against a root, never trust IDs from the wire. |
| Gitea | Go; `x/crypto/ssh` direct; `gitea serv`; git CLI ≥2.25; per-repo hooks → internal HTTP | Version-gated feature detection; hooks call back into the app. |
| gogs | Go; `x/crypto/ssh` direct; `gogs serv`; `ssh-keygen` for host keys | Simplest working exec-only SSH server; read `internal/ssh/ssh.go`. |
| git.sr.ht | Python web + Go `cmd/shell`, `cmd/update-hook`, `cmd/http-clone`; system sshd + `AuthorizedKeysCommand` dispatcher | Command whitelist, path canonicalization, SQL-based access decision, `syscall.Exec` into git with a JSON push context env var. Our SSH server replaces sshd but the shell logic maps 1:1. |
| Gitolite | Perl over sshd | `SSH_ORIGINAL_COMMAND` parsing must accept `git-upload-pack` and `git upload-pack`; write = receive-pack only. |
| stagit | C + libgit2, static HTML generator | Pre-render on `post-receive`; the Gemini server then serves static gemtext for the hot paths. Strong fit for a low-resource Gemini forge. |
| cgit / klaus | C CGI on git's sources / Python dulwich | Dynamic viewers; cgit's caching layer is the model if we render on demand. |
| molly-brown, agate, GmCapsule | Go / Rust / Python Gemini servers | Cert-fingerprint zones; rustls SNI resolver; Titan defaults (cert required, 10 MiB, handler after full upload). |

## 13. Scoring

Weights reflect this project's stated constraints (static binary, small VPS,
identity via TLS certs, restricted SSH). 1–5, higher is better.

| Criterion | Weight | Go | Rust | Notes |
|---|---|---|---|---|
| Gemini/Titan server effort | 2 | 5 | 4 | Both custom; Go stdlib TLS needs no verifier trait |
| TLS client-cert handling | 3 | 4 | 4 | Go: strict x509 parse at handshake; Rust: custom `danger` verifier |
| SSH server (maturity, algos incl. sk-*, security response) | 3 | 5 | 3 | x/crypto/ssh has sk-* and Go-team CVE cadence; russh solid but smaller |
| Git transport (shell-out) | 2 | 5 | 5 | Identical approach |
| Git object browsing | 2 | 3 | 4 | gitoxide > go-git; CLI plumbing equalizes |
| SQLite + migrations | 2 | 5 | 4 | modernc is cgo-free; rusqlite needs cc for musl |
| Static binary / cross-compile | 3 | 5 | 3 | `CGO_ENABLED=0` vs musl + C toolchain |
| Memory / binary size | 1 | 3 | 5 | Rust smaller; both fine at 1 GB |
| Concurrency fit (idle conns + subprocess piping) | 2 | 5 | 4 | goroutines + io.Copy vs callback Handler |
| Observability | 1 | 4 | 4 | slog+client_golang vs tracing+prometheus-client |
| Supply chain surface | 3 | 5 | 3 | ~30 modules, stdlib crypto vs 300–500 crates, build scripts |
| Prior art to copy | 2 | 5 | 2 | soft-serve/Gitea/gogs/sr.ht are Go |
| Build time / iteration speed | 1 | 5 | 3 | seconds vs minutes |
| **Weighted total** | | **4.7** | **3.6** | |

## 14. Starter dependency list

### Go (recommended) — matches the current `go.mod` (`module as215520.net/forge`, `go 1.27.1`)

```
require (
    golang.org/x/crypto v0.57.0                   // ssh server (2026-09-08; includes the CVE-2026-56855/78662 fixes)
    modernc.org/sqlite v1.58.0                    // cgo-free SQLite 3.53.4 (v1.59.0 of 2026-09-05 is a minor bump; either is fine)
    github.com/prometheus/client_golang v1.24.1   // metrics (2026-07-24)
    github.com/BurntSushi/toml v1.6.0             // config file (already chosen by pkg/config)
)

// add when the store lands
//   github.com/pressly/goose/v3 v3.28.0         // embedded SQL migrations; or a 50-line schema_version table

// deliberately NOT used
//   git.sr.ht/~adnano/go-gemini                 // pkg/gemini already implements Gemini+Titan on crypto/tls
//   charm.land/ssh, gliderlabs/ssh              // exec-only server on x/crypto/ssh needs no framework
//   go-git, git2go, gogs/git-module             // ADR 0004: git CLI via internal/vcs/git
//   cobra / urfave/cli                          // stdlib flag + subcommand switch until the admin CLI grows

// dev tooling (not in go.mod)
//   golang.org/x/vuln/cmd/govulncheck@v1.8.0
//   honnef.co/go/tools/cmd/staticcheck@latest
//   github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

Runtime requirement: `git` ≥ 2.43 (ADR 0004); `cat-file --batch-command`
needs ≥ 2.36, so that is covered.

### Rust (if overruled)

```
tokio = "1.53"            rustls = "0.23.44"      tokio-rustls = "0.26.5"
x509-parser = "0.18.1"    russh = "0.63.3"        gix = "0.87.1"  (browsing only)
rusqlite = { version = "0.40.2", features = ["bundled"] }
refinery = { version = "0.9.2", features = ["rusqlite"] }
clap = { version = "4.6.6", features = ["derive"] }
tracing = "0.1.44"        tracing-subscriber      prometheus-client = "0.25.1"
tools: cargo-audit 0.22.2, cargo-deny 0.20.2, cargo-vet; target x86_64/aarch64-unknown-linux-musl via cargo-zigbuild
```

## 15. Proposed Go project layout

Aligned with what already exists in the tree (`pkg/gemini`,
`internal/vcs`, `pkg/config`, `internal/version`); new packages marked *(new)*.

```
forge/                          module as215520.net/forge
├── cmd/forge/main.go           (new) one binary: `forge serve`, `forge hook <name>`, `forge admin …`
├── internal/
│   ├── version/                build version injected via -ldflags
│   ├── config/                 TOML config (BurntSushi/toml), validation
│   ├── gemini/                 Gemini + Titan on crypto/tls: request parsing (≤1024B+CRLF, ;size=;mime=;token=),
│   │                           ResponseWriter, status constants, deadlines, client-cert fingerprint, gemtext helpers
│   ├── tlsid/          (new)   tls.Config builder (RequestClientCert, GetCertificate/SNI), host cert generation
│   │                           and storage, fingerprint → identity lookup, cert-registration policy (60/61/62)
│   ├── sshd/           (new)   x/crypto/ssh server: host keys, PublicKeyCallback (+ post-auth fingerprint re-check),
│   │                           session-only / exec-only, command whitelist (both `git-upload-pack` and
│   │                           `git upload-pack` spellings), path canonicalization, exit-status
│   ├── vcs/                    VCS interface (refs, tree, blob, log, diff, bundle, fetch, fsck)
│   │   └── git/        (new)   git CLI adapter: fixed env (GIT_CONFIG_NOSYSTEM, GIT_CONFIG_GLOBAL=/dev/null,
│   │                           core.hooksPath), transport spawn with io.Copy, cat-file --batch-command reader,
│   │                           process semaphore + timeouts, pack.* memory caps
│   ├── hooks/          (new)   pre-receive/update/post-receive entrypoints + unix-socket RPC to the daemon
│   ├── store/          (new)   sqlite open (WAL pragmas, write pool of 1 + read pool), embedded migrations, queries
│   ├── auth/           (new)   users, ssh keys, cert bindings, access decisions (read/write/manage)
│   ├── repo/           (new)   repo model, on-disk layout, create/rename/delete, redirects
│   ├── render/         (new)   gemtext pages (index, tree, blob, log, commit, diff, refs, feeds);
│   │                           stagit-style pre-render on post-receive + on-demand fallback
│   └── metrics/        (new)   prometheus registry, localhost-only /metrics + pprof
├── migrations/         (new)   0001_init.sql …
├── docs/                       ADRs, research, threat model, vcs-interface.md
└── infra/                      OpenTofu, network state
```

Rough effort for the new pieces: tlsid 150 lines, sshd 300–400, vcs/git
800–1200, hooks 200, store 400–600, render open-ended. A first vertical slice
(clone over SSH + browse a tree over Gemini with cert identity) is a few days
of work in Go.

## 16. Sources

- Gemini spec: https://geminiprotocol.net/docs/protocol-specification.gmi
- Titan spec (via proxy): https://portal.mozz.us/gemini/transjovian.org/titan/The%2520Titan%2520Specification
- GmCapsule manual (Titan defaults): https://geminispace.org/gmcapsule/gmcapsule.html
- go-gemini: https://pkg.go.dev/git.sr.ht/~adnano/go-gemini ; versions tab
- windmark: https://crates.io/crates/windmark ; https://github.com/gemrest/windmark
- twinstar: https://crates.io/crates/twinstar ; titanite: https://crates.io/crates/titanite
- agate main.rs: https://raw.githubusercontent.com/mbrubeck/agate/master/src/main.rs
- Go crypto/tls: https://pkg.go.dev/crypto/tls ; handshake_server.go: https://go.dev/src/crypto/tls/handshake_server.go
- Go GODEBUG history: https://go.dev/doc/godebug ; issues #40521, #56371, #77379
- Go 1.26/1.27 notes: https://go.dev/doc/go1.26 ; https://go.dev/doc/go1.27
- rustls ClientCertVerifier: https://docs.rs/rustls/latest/rustls/server/danger/trait.ClientCertVerifier.html ; crates.io/crates/rustls
- x/crypto/ssh: https://pkg.go.dev/golang.org/x/crypto/ssh ; advisories https://groups.google.com/g/golang-announce/c/1y3fb2np35U ; NCC report https://www.nccgroup.com/research/public-report-go-xcryptossh-cryptographic-implementation-review/
- gliderlabs/ssh releases: https://api.github.com/repos/gliderlabs/ssh/releases/latest ; charmbracelet/ssh: https://api.github.com/repos/charmbracelet/ssh/releases/latest
- russh: https://crates.io/crates/russh ; https://github.com/Eugeny/russh ; https://docs.rs/russh/latest/russh/server/trait.Handler.html ; https://stack.watch/product/russhproject/russh/
- go-git releases: https://github.com/go-git/go-git/releases ; gitoxide status: https://github.com/GitoxideLabs/gitoxide/blob/main/crate-status.md ; git2go: https://github.com/libgit2/git2go
- modernc sqlite: https://pkg.go.dev/modernc.org/sqlite ; https://gitlab.com/cznic/sqlite/-/raw/master/CHANGELOG.md ; benchmark https://datastation.multiprocess.io/blog/2022-05-12-sqlite-in-go-with-and-without-cgo.html
- goose: https://pkg.go.dev/github.com/pressly/goose/v3 ; golang-migrate: https://pkg.go.dev/github.com/golang-migrate/migrate/v4 ; refinery: https://crates.io/crates/refinery ; rusqlite: https://crates.io/crates/rusqlite
- Litestream: https://github.com/benbjohnson/litestream/releases ; https://litestream.io/reference/vfs/
- client_golang: https://pkg.go.dev/github.com/prometheus/client_golang ; prometheus-client: https://crates.io/crates/prometheus-client ; tracing: https://crates.io/crates/tracing
- cobra / urfave cli v3 / clap: pkg.go.dev and crates.io version pages
- govulncheck: https://pkg.go.dev/golang.org/x/vuln ; cargo-audit / cargo-deny: crates.io ; Cargo CVE-2026-5223: https://blog.rust-lang.org/2026/05/25/cve-2026-5223/ ; Aug 2026 crate compromise: https://thehackernews.com/2026/08/rust-supply-chain-attack-puts-build.html
- soft-serve: https://github.com/charmbracelet/soft-serve (go.mod, pkg/ssh/ssh.go, pkg/ssh/middleware.go, pkg/git/service.go, release v0.12.2)
- Gitea: https://github.com/go-gitea/gitea (modules/ssh/ssh.go, modules/git/git.go) ; gogs: https://github.com/gogs/gogs (internal/ssh/ssh.go)
- git.sr.ht shell: https://git.sr.ht/~sircmpwn/git.sr.ht/tree/master/item/cmd/shell/main.go
- Gitolite: https://gitolite.com/gitolite/how.html
- molly-brown: https://pkg.go.dev/github.com/LukeEmmet/molly-brown
- Go vs Rust size/memory (order of magnitude only): https://www.stanza.dev/compare/go-vs-rust ; https://rustify.rs/articles/rust-vs-go-2026
