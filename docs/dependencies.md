# Dependency review

The binary is built with `CGO_ENABLED=0` from the Go standard library plus
a deliberately short list of modules. `go.sum` pins every module hash;
`govulncheck` runs in `make lint` and CI. Reviewed 2026-09-10.

## Direct dependencies

| Module | Why | Alternatives considered |
| --- | --- | --- |
| `golang.org/x/crypto` | SSH server (`ssh`), key parsing, fingerprints. Maintained by the Go team; receives fast CVE fixes. | gliderlabs/ssh (frozen), charm.land/ssh (heavier) |
| `modernc.org/sqlite` | cgo-free SQLite: static binary, no C toolchain. Pulls `modernc.org/libc`, `mathutil`, `memory`. | mattn/go-sqlite3 (cgo), PostgreSQL (operational cost) |
| `github.com/prometheus/client_golang` | Metrics registry and exposition. Pulls `client_model`, `common`, `procfs`, `protobuf`, `beorn7/perks`, `cespare/xxhash`, `munnerz/goautoneg`. | hand-written text exposition (would drop 8 modules; revisit if the tree must shrink) |
| `github.com/BurntSushi/toml` | Configuration parsing with unknown-key detection. | pelletier/go-toml (fine; either works) |

Transitive-only: `dustin/go-humanize`, `google/uuid`, `remyoudompheng/bigfft`
(from modernc), `golang.org/x/sys`.

Total modules in `go list -m all`: 60, of which 28 packages are linked into
the binary. No dependency performs network I/O on its own; none executes
external programs except our own `internal/vcs/git` calling `git`.

## Runtime dependencies

- `git` >= 2.43 (transport, plumbing, `merge-tree --write-tree`).
- Optional: `hg` for the Mercurial adapter prototype (`docs/mercurial.md`).
- Nodes: `bird2`, `wireguard-tools`, `nftables`, `age` (see cloud-init).

## Supply-chain controls

- Toolchain pinned in `go.mod` (`go 1.27.1`); CI uses `go-version-file`.
- `go.sum` verified on every build; `GOFLAGS=-mod=mod` with the module
  proxy's checksum database (`GONOSUMDB` is not set).
- `govulncheck ./...` in `make lint`; `staticcheck` for API misuse.
- Release binaries are reproducible with `-trimpath` and a pinned toolchain;
  `SHA256SUMS` are signed with the maintainer's SSH key
  (`docs/release-procedure.md`).
- Adding a dependency requires an ADR note when it links into the daemon.

## Upgrade cadence

`go get -u ./... && go mod tidy` monthly, or immediately on a `govulncheck`
finding; re-run `make check` and the acceptance tests before tagging.
