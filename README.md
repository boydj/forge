# forge

A small, Internet-native source-code forge built from protocols that each do one job well.

- **Gemini** for browsing: repositories, source trees, history, diffs, issues, reviews, releases.
- **Titan** for human writes: issues, comments, reviews, metadata, release assets.
- **Git over SSH** for clone, fetch and push with ordinary Git clients.
- **Gemfeeds** for subscriptions and activity.
- **TLS client certificates** as human identity; **SSH public keys** as Git identity.
- **DNS**, **BGP** and **anycast** to run the same service from several points of presence.

No HTTP, HTML, JavaScript, cookies or OAuth are involved in using the forge.

```
gemini://git.as215520.net/~alice/project/
git clone git@git.as215520.net:alice/project.git
```

## Status

Early development. See [docs/status.md](docs/status.md) for the current milestone,
what works, what is tested and what is blocked.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/forge` | the single `forge` binary (`forge serve`, `forge admin ...`) |
| `internal/` | application packages (Gemini, Titan, SSH, Git adapter, store, rendering) |
| `migrations/` | SQLite schema migrations |
| `tests/` | integration and protocol tests |
| `docs/` | architecture, protocol, network, operations and decision records |
| `infra/` | OpenTofu, configuration management, BIRD, WireGuard, nftables, systemd, monitoring, backup |
| `scripts/` | developer and operator tooling |

## Getting started

```
make dev      # install toolchain pins and pre-commit checks
make build    # build ./bin/forge
make test     # unit tests
make run      # run a local forge on gemini://localhost:1965/ and ssh://localhost:2222
```

See [docs/development.md](docs/development.md).

## Documentation

- [Architecture](docs/architecture.md)
- [Protocol architecture](docs/protocol-architecture.md)
- [Network architecture](docs/network-architecture.md)
- [Threat model](docs/threat-model.md)
- [Replication](docs/replication.md)
- [Operations](docs/operations.md)
- [Decision records](docs/decisions/)

## License

Apache-2.0. See [LICENSE](LICENSE).
