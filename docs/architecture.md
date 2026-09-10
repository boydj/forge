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
| `internal/gemini` | Gemini and Titan protocol: request parsing (incl. Titan parameters and `;edit`), responses, gemtext writer, TLS setup |
| `internal/tlsid` | long-lived self-signed service certificate |
| `internal/sshd` | restricted SSH server |
| `internal/hooks` | git hook protocol (pre/post-receive over a Unix socket, proc-receive pkt-line) |
| `internal/vcs` | VCS interface; `internal/vcs/git` implements it with the git CLI |
| `internal/store` | SQLite store, migrations, event log, replication cursors |
| `internal/forge` | domain services: accounts, repositories, issues, changes, releases, ACL, quotas, hooks, backup |
| `internal/web` | Gemini/Titan handlers, gemtext rendering, gemfeeds and Atom, write forwarding |
| `internal/repl` | replication (events, metadata, git over the control network) and leadership |
| `internal/health` | health checks and hysteresis-controlled announcement |
| `internal/metrics` | Prometheus registry |

Details: `docs/protocol-architecture.md`, `docs/vcs-interface.md`,
`docs/replication.md`, `docs/network-architecture.md`, `docs/threat-model.md`.

## Request flows

How a request moves through the packages above. Error mapping to Gemini
statuses is centralised in `internal/web` (`request.fail`); see
`docs/titan.md` for the write rules and `docs/git-ssh.md` for SSH.

### Gemini read

```
client --TLS 1965--> gemini.Server.handleConn
  handshake (10 s), read request line (<= 1024 B), parse URL, expose client cert
  -> web.Handler.ServeGemini
       forge.Authenticate(cert): SPKI -> certificates row -> user (62/61 on revoked/expired/disabled)
       route by path (handler.go / repo.go)
         forge.LookupRepo(viewer, owner, name)   -> store (private repos read as not found)
         vcs.Repository via forge.Open            -> git plumbing with git.Backend.Env(), timeouts, slots
         gemini.Page builder                      -> escaped gemtext
       <- 20 text/gemini (or 10 INPUT, 30 redirect, 5x/6x)
  flush, close_notify, log "request", metrics
```

INPUT prompts (`10`) round-trip through the query string: `/new?name`,
`/account?name`, `/…/close?close comment`. Only the confirmations listed in
`docs/titan.md` change state on a Gemini request.

### Titan write

```
client --TLS 1965--> gemini.Server.handleConn
  parse titan URL: last-segment ;size=N;mime=T[;token][;edit]  (59 on any deviation)
  size > limits.max_titan_bytes -> 59 before reading a byte
  body = LimitReader(size), 120 s deadline
  -> web.Handler.ServeGemini -> serveTitan
       requireUser: no cert 60, unregistered 61        (still no body read)
       dispatch (/account/keys, /~o/r/issues/...)
       readTitanText: per-endpoint limit 50, MIME allowlist 50, exact length 59, UTF-8 59
       ;edit -> 20 text/plain current text, return
       forge.<Action>(user, access, text): permission, archived, leader, limits, store tx, event
       <- 30 <gemini path of the result>
```

### Git over SSH (push)

```
git client --SSH 22--> sshd.Server
  caps, handshake (20 s), public-key auth only -> forge.UserForSSHKey (fingerprint lookup)
  session channel, exec "git-receive-pack 'owner/repo.git'"  -> ParseCommand grammar
  Authorizer: forge.LookupRepo + CanWrite + not archived + IsLeader -> disk path
  exec git receive-pack -- <path>  with git.Backend.Env() + FORGE_* variables
    git runs <data_dir>/hooks/pre-receive  = `forge hook pre-receive`
      -> JSON over hook.sock -> forge.HandleHook: role, archived, leader, free disk,
         repo/user quota vs quarantine size, ref namespace, default-branch deletion  -> allow/deny
    refs updated
    git runs post-receive = `forge hook post-receive`
      -> forge.postReceive: one event per ref (created/pushed/tagged/deleted), RecordPush(size), OnPush
  exit-status, log "session", metrics
```

Clone and fetch follow the same path with `git-upload-pack` (read access
suffices; no hooks run).

### Admin CLI and timers

`forge admin …` opens the same SQLite file (WAL) and repository store as
the daemon and calls `internal/forge`/`internal/store` directly; there is
no RPC to the running process. `forge-maintenance.timer` runs
`forge admin maintenance` nightly; the daemon additionally purges deleted
repositories and expired tokens every hour in-process.
