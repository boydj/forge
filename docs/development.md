# Development

How to build, run and test the forge on a laptop, and the conventions the
code follows. Operator material is in `docs/operations.md`; protocol detail
in `docs/protocol-architecture.md`, `docs/titan.md`, `docs/gemfeeds.md` and
`docs/git-ssh.md`.

## Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| Go | as pinned in `go.mod` (currently 1.27.1) | the only build dependency; `CGO_ENABLED=0`, static binary |
| git | >= 2.43 | runtime dependency (ADR 0004); the adapter uses `--end-of-options`, `GIT_CONFIG_COUNT`, `receive-pack --` |
| ssh, ssh-keygen | OpenSSH | `internal/sshd` tests and the acceptance test drive the real client |
| openssl | 1.1.1+ / 3.x | hand-testing Gemini and Titan (`s_client`), generating client certificates |
| python3 + PyYAML | 3.11+ | `scripts/netgen`, `tests/network`, `tests/infra`, `make infra-lint` |
| staticcheck, govulncheck | latest | `make tools` (part of `make dev`) installs them into `$GOPATH/bin` |
| tofu, sops, age | latest | optional; `make tools-infra` installs them into `~/.local/bin` without root |

The Makefile prepends `~/.local/go/bin`, `~/go/bin`, `~/.local/bin` and
`./bin` to `PATH`, so a toolchain unpacked under `~/.local/go` works
without shell configuration.

## Make targets

| Target | What it does |
| --- | --- |
| `make dev` | `make tools` then prints the versions of go, git, tofu, sops, age (missing infra tools are reported, not fatal) |
| `make build` | `go build -trimpath -ldflags '-s -w -X .../version.Version=<git describe>'` to `./bin/forge` |
| `make test` | unit tests with `-race -count=1` over `./...` (needs `CGO_ENABLED=1` for the race detector; the Makefile sets it) |
| `make integration` | `make build` then `go test -tags integration ./tests/...` (real git, ssh, Gemini, Titan) |
| `make lint` | `go vet`, gofmt check, staticcheck, govulncheck, then `scripts/infra-lint` |
| `make infra-lint` | tofu fmt/validate where tofu exists, `scripts/netgen --check`, network tests, YAML parse of `infra/` |
| `make check` | `lint test integration`: exactly what CI runs |
| `make run` | `make build` then `scripts/dev-run` (see below) |
| `make fmt`, `make vet`, `make tidy`, `make clean` | the usual |
| `make tools`, `make tools-infra` | install developer / infrastructure tools |

CI (`.github/workflows/ci.yml`) only calls `make` targets and `scripts/*`,
so anything green locally is green in CI and vice versa.

## Running locally

`make run` calls `scripts/dev-run`, which initialises `var/dev` once and
then serves:

```
./bin/forge admin init --data var/dev --hostname localhost --write-config var/dev/forge.toml
./bin/forge serve --config var/dev/forge.toml
```

`admin init` without listen flags writes the production defaults
(`gemini.listen = [":1965"]`, `ssh.listen = [":22"]`), and port 22 needs
privileges. For an unprivileged developer node initialise by hand with high
ports before the first `make run`, or use a separate directory:

```
./bin/forge admin init --data var/dev --hostname localhost \
    --gemini-listen :1965 --ssh-listen :2222 --write-config var/dev/forge.toml
./bin/forge serve --config var/dev/forge.toml
```

`--ssh-port` / `--gemini-port` default to the port of the first listen
address, so clone URLs and Titan URLs on the pages come out right
(`ssh://git@localhost:2222/owner/repo.git`). `forge.toml` is only written
when absent; delete it (or `var/dev`) to re-initialise. Environment
overrides: `FORGE_CONFIG`, `FORGE_DATA_DIR`, `FORGE_NODE`, `FORGE_LOG_LEVEL`.

On first start the daemon generates a self-signed P-256 certificate under
`var/dev/tls/` and an ed25519 host key under `var/dev/ssh/`, writes the hook
scripts into `var/dev/hooks/`, and logs the certificate fingerprint and the
SSHFP records.

The first account registered through `/account` becomes the administrator.
Accounts, keys and repositories can also be created from the CLI while the
daemon runs (SQLite WAL allows it):

```
./bin/forge admin --config var/dev/forge.toml user create alice
./bin/forge admin --config var/dev/forge.toml key add alice ~/.ssh/id_ed25519.pub
./bin/forge admin --config var/dev/forge.toml repo create alice/proj --description "hello"
git clone ssh://git@localhost:2222/alice/proj.git
```

### Trying it with Lagrange

1. Open `gemini://localhost/` (add `:1965` only if the port differs).
   Accept the self-signed certificate (TOFU).
2. Follow "sign in or register" (`/account`); the server answers `60`.
   Create a new identity and, in the identity dialog, scope it to the
   **whole domain** ("localhost"), not just the current page. Titan
   requests reuse the identity that matches the same `gemini://` URL
   prefix, so a page-scoped identity would not be offered for uploads.
3. Choose "Create a new account", type a username at the `10` prompt.
4. `/new` asks for a repository name; `/account/keys/add` pastes one SSH
   key. For a whole `authorized_keys` file use Titan: on `/account/keys`,
   "Upload" (page menu / Ctrl+U) to `titan://localhost/account/keys`.
5. Open an issue: on `/~alice/proj/issues/new` upload text with Titan.
   Lagrange sends typed text as `text/plain`; the first line is the title.
   Editing an issue: "Edit page with Titan" on `.../issues/1/edit` uses the
   `;edit` request to load the current text.

### Trying it with openssl

Gemini read (the `-servername` matters: it is the SNI):

```
printf 'gemini://localhost:1965/\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername localhost
printf 'gemini://localhost:1965/status\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername localhost
```

Generate a client certificate (any self-signed certificate works; the
identity is the SHA-256 of its public key) and register:

```
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -keyout client.key -out client.crt -days 3650 -subj /CN=alice
C="-connect 127.0.0.1:1965 -servername localhost -cert client.crt -key client.key"
printf 'gemini://localhost:1965/account\r\n'        | openssl s_client -quiet $C   # 20: registration page
printf 'gemini://localhost:1965/account?alice\r\n'  | openssl s_client -quiet $C   # 30 /account
printf 'gemini://localhost:1965/new?proj\r\n'       | openssl s_client -quiet $C   # 30 /~alice/proj/
```

Titan upload: request line, then exactly `size` bytes of body on the same
connection.

```
printf 'First line is the title\n\nBody.\n' > body.txt
N=$(wc -c < body.txt)
{ printf 'titan://localhost:1965/~alice/proj/issues/new;size=%d;mime=text/plain\r\n' "$N"; cat body.txt; } \
    | openssl s_client -quiet -nocommands $C            # 30 /~alice/proj/issues/1
printf 'titan://localhost:1965/~alice/proj/issues/1/edit;edit\r\n' \
    | openssl s_client -quiet -nocommands $C            # 20 text/plain: title, blank line, body
```

`-nocommands` is advisable: without it `s_client` treats input lines that
begin with `Q`, `R`, `k`, `K` or `B` as its own commands when they arrive in
a separate read. In testing against a local node the upload also succeeded
without the flag (the whole pipe arrives in one read) and with `-ign_eof`
alone, but do not rely on that. `-quiet` already implies `-ign_eof`, which
keeps the connection open until the server closes it, so the status line is
always printed.

SSH sanity check (`shell request failed` / `forge: interactive shell not
available; use git` is the expected reply for a registered key;
`Permission denied (publickey)` for an unregistered one):

```
ssh -p 2222 git@localhost
```

## Code layout

| Path | Contents |
| --- | --- |
| `cmd/forge` | `main.go` (subcommands), `serve.go` (wiring, listeners, hourly maintenance loop, hook script installation), `ssh.go` (SSH auth/authz adapters, hook socket), `admin.go` (CLI), `hook.go` (`forge hook`) |
| `pkg/config` | TOML schema, defaults, validation, derived paths and URL builders |
| `pkg/gemini` | protocol server: request-line parsing (Gemini and Titan), response writer with META sanitising, gemtext `Page` builder, TLS config, connection limits |
| `internal/web` | routing and pages (`handler.go`, `front.go`, `repo.go`, `issues.go`, `account.go`, `feeds.go`, `markdown.go`, `stubs.go` for M3 placeholders); the only package that knows URL layout |
| `internal/forge` | domain services: identity, registration, enrolment codes, SSH keys, repositories, permissions, quotas, issues/comments, push hooks, events |
| `internal/store` | SQLite access and migrations; **all SQL lives here** |
| `internal/vcs` | the VCS interface; `internal/vcs/git` implements it with the git CLI (`runner.go` hardened environment, `repo.go` plumbing) |
| `internal/sshd` | restricted SSH server (`docs/git-ssh.md`) |
| `internal/hooks` | JSON-over-Unix-socket protocol between `forge hook` and the daemon |
| `pkg/tlsid` | service certificate generation/loading |
| `pkg/metrics` | Prometheus registry |
| `internal/version` | `Version` set by `-ldflags` |
| `migrations/` | `NNNN_name.sql`, embedded and applied in order at startup |
| `tests/` | `acceptance_test.go` (build tag `integration`), `network/` and `infra/` Python tests |
| `infra/`, `scripts/` | deployment, network generation, secrets, backup |

`docs/architecture.md` lists some packages (`internal/titan`,
`internal/feed`, `internal/repl`, `pkg/health`) that are planned but do
not exist yet: Titan parsing lives in `pkg/gemini`, feeds in
`internal/web/feeds.go`, and replication/health arrive with M5.

## Testing tiers

1. **Unit and protocol tests** (`make test`). `pkg/gemini` parses
   request lines and drives a real TLS listener; `internal/web/web_test.go`
   starts the handler on a loopback port and performs Gemini and Titan
   requests with generated client certificates (registration, enrolment,
   key upload, issues, `;edit`, size and MIME rejections); `internal/sshd`
   runs the system `git` and `ssh` against the server (skipped when
   absent); `internal/vcs/git`, `internal/store`, `internal/forge`,
   `pkg/config`, `pkg/tlsid` test their own packages.
2. **Integration acceptance** (`make integration`). `tests/acceptance_test.go`
   is the canonical "does it work" check: it builds nothing itself (skips if
   `bin/forge` is missing), initialises a node on random loopback ports,
   starts `forge serve`, creates users and a repository with the CLI, then
   with ordinary `git` over `ssh`: clone, push a commit and a tag, browse the
   overview/blob/refs/feed pages over Gemini, push again and check the feed
   entry, verify a read-only user cannot push, that deleting the default
   branch and pushing `refs/evil/x` are refused by the pre-receive hook,
   that a private repository is invisible (`51`) and uncloneable to others,
   that an unknown key and a shell command are rejected, and finally runs
   `admin status` and `admin backup`. A restore test will join it later.
3. **Infrastructure tests**: `python3 -m unittest discover -s tests/network`
   (address plan, BIRD/WireGuard generation, `bird -p` when installed) and
   `-s tests/infra` (cloud-init template renders to valid YAML);
   `make infra-lint`.

## Conventions

- **Security invariants** in `docs/threat-model.md` are not negotiable
  without an ADR: no shell over SSH, only `git-upload-pack`/`receive-pack`
  with a validated path; no Gemini request mutates state unless the user
  typed a confirmation into an INPUT prompt; every Titan write requires a
  registered client certificate and is checked before the body is read;
  git is always run with `git.Backend.Env()` (`docs/operations.md` lists it)
  and fixed argument lists ending in `--end-of-options`/`--`.
- **No SQL outside `internal/store`.** Handlers call `internal/forge`, which
  calls the store. Migrations are additive files in `migrations/`.
- **No user string reaches a response header unescaped.** `Header()` runs
  META through `SanitizeMeta` (CR/LF stripped, 1024-byte cap); `Page.Text`,
  `Link`, `Heading` escape line-leading syntax; user-authored gemtext goes
  through `web.userGemtext`, which demotes headings and marks links
  `[user link]`; raw blobs are served with a sniffed MIME, never as
  `text/gemini` unless the name ends in `.gmi`/`.gemini`.
- **Names** are validated once (`forge.ValidUserName`, `ValidRepoName`,
  ADR 0009); disk paths are built only from validated names.
- **Errors** are sentinel values in `internal/forge` mapped to statuses in
  one place (`request.fail`); do not write status codes ad hoc.
- gofmt, `go vet`, staticcheck and govulncheck must be clean (`make lint`).

### Adding a page

1. Add the route: top level in `Handler.route` (`internal/web/handler.go`),
   per-user or per-repository in `userRoutes` (`repo.go`); keep the URL in
   ADR 0009's shape and reserve any new fixed segment in
   `forge.reservedRepoNames`/`reservedUserNames`.
2. Resolve access first (`h.F.LookupRepo` returns `ErrNotFound` for
   private repositories the viewer cannot see).
3. Build with `req.page(title)`, `p.Text/Link/Heading/Pre/Item`, end with
   `h.footer(p, req)` and `req.send(p)`; use `req.fail(err)` for errors.
4. Trailing-slash rule: directory-like pages redirect (`31`) to the slash
   form so relative links resolve.
5. Add a case to `internal/web/web_test.go`.

### Adding a Titan endpoint

1. Dispatch from `serveTitan` (`account.go`) or `titanRepo` -> the
   per-area function (`titanIssues` is the model). Identity is already
   checked (`requireUser`) before you are called.
2. For text: `body, ok := h.readTitanText(req, limit)` enforces the
   per-endpoint size (`50`), MIME allowlist (`text/plain`, `text/gemini`,
   `text/markdown`), exact length (`59`), UTF-8 (`59`) and normalises CRLF.
   For anything else, read `req.Body` yourself, bounded by
   `req.Titan.Size`, and stream to `Config.TmpDir()` before committing.
3. If the resource is editable, answer `req.Titan.Edit` with
   `20 text/plain` and the current raw text before reading a body.
4. Call a domain method in `internal/forge` (permission, archived, leader,
   limits, event) and finish with `gemini.Redirect(req.w, geminiPath)`.
   Never redirect to a `titan://` URL (Lagrange reopens the upload dialog).
5. Show the `titan://` URL on the corresponding Gemini page
   (`h.F.Config.TitanURL(path)`), document it in `docs/titan.md`, and add a
   test.

## Commits

Conventional prefixes (`CONTRIBUTING.md`): `feat:`, `fix:`, `docs:`,
`test:`, `infra:`, `net:`, `ops:`, `chore:`; one logical change per commit.
Decisions go in `docs/decisions/` as ADRs.
