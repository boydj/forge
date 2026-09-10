# Architecture

One binary, `forge`, runs on every node. A node serves Gemini and Titan on
port 1965, Git over SSH on port 22, and exposes Prometheus metrics on a
control-network address. Metadata is in SQLite; content is in bare Git
repositories. Nodes are joined by a WireGuard mesh. Each repository has one
leader node; others are replicas.

```
                  Internet (anycast IPv4 /24, IPv6 /48 from AS215520)
                                     |
            +------------------------+------------------------+
            |                        |                        |
        +---+----+               +---+----+               +---+----+
        | POP A  |               | POP B  |               | POP C  |
        | forge  |<--WireGuard-->| forge  |<--WireGuard-->| forge  |
        | bird   |               | bird   |               | bird   |
        +--------+               +--------+               +--------+
         sqlite                   sqlite                   sqlite
         repos/                   repos/                   repos/
```

## Process structure (`forge serve`)

```
 listener :1965 (TLS) --> gemini/titan front --> router --> handlers
 listener :22   (SSH) --> key auth --> command guard --> git-upload-pack / git-receive-pack
 unix socket           --> hook RPC (pre-receive ACL, post-receive events)
 replication worker    --> fetch from leaders, apply metadata events
 health worker         --> local checks -> BIRD announce/withdraw with hysteresis
 metrics :9100 (control network only)
```

## Packages

| Package | Role |
| --- | --- |
| `internal/config` | TOML configuration and defaults |
| `internal/gemini` | Gemini protocol: request parsing, responses, gemtext writer, TLS setup |
| `internal/titan` | Titan request parsing and bounded body reading |
| `internal/sshd` | restricted SSH server |
| `internal/vcs` | VCS interface; `internal/vcs/git` implements it with the git CLI |
| `internal/store` | SQLite store, migrations, event log |
| `internal/forge` | domain services: accounts, repositories, issues, changes, releases, ACL, quotas |
| `internal/web` | Gemini/Titan handlers and gemtext rendering (the "web" of small protocols) |
| `internal/feed` | Gemfeed generation |
| `internal/repl` | replication and leadership |
| `internal/health` | health checks and announcement control |
| `internal/metrics` | Prometheus registry |

Details: `docs/protocol-architecture.md`, `docs/vcs-interface.md`,
`docs/replication.md`, `docs/network-architecture.md`, `docs/threat-model.md`.
