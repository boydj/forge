# Operations

Running a forge node day to day. Installation is in `docs/self-hosting.md`,
secrets in `docs/secrets.md`, TLS and host keys in `docs/tls.md`, backups
and restores in `docs/disaster-recovery.md`, the multi-POP network in
`docs/network-architecture.md`.

## Configuration reference

One TOML file (`/etc/forge/forge.toml`; `forge serve --config`,
`FORGE_CONFIG`). Unknown keys are rejected at load. Defaults are those of
`config.Default` (`internal/config/config.go`); `forge admin init` writes a
complete file. Sizes are bytes; durations are Go strings (`30s`, `5m0s`).

| Key | Default | Meaning |
| --- | --- | --- |
| `node` | `local` | node name, unique in the deployment; stamped on events, used for leadership. Override: `FORGE_NODE` |
| `hostname` | `localhost` | public service name used in generated `gemini://`, `titan://` and clone URLs; the first SAN of a generated certificate. Any SNI is still served |
| `title` | `forge` | name shown on pages and feed titles |
| `data_dir` | `/var/lib/forge` | database, repositories, assets, tmp, TLS and SSH identities. Override: `FORGE_DATA_DIR` |
| `log_level` | `info` | `debug`, `info`, `warn`, `error`. Override: `FORGE_LOG_LEVEL` |
| `log_format` | `text` | `text` or `json` (slog to stderr, i.e. the journal) |
| `gemini.listen` | `[":1965"]` | Gemini/Titan TLS listeners (list) |
| `gemini.cert_file` | `<data_dir>/tls/server.crt` | service certificate (PEM); generated if the key file is absent |
| `gemini.key_file` | `<data_dir>/tls/server.key` | service private key (PEM) |
| `gemini.port` | `1965` | port written into generated URLs when not 1965 |
| `ssh.listen` | `[":22"]` | Git-over-SSH listeners |
| `ssh.host_key_file` | `<data_dir>/ssh/host_ed25519` | ed25519 host key (OpenSSH format); generated if absent, `.pub` written beside it |
| `ssh.port` | `22` | port written into clone URLs when not 22 (`ssh://` form) |
| `ssh.user` | `git` | user shown in clone URLs; the login name is ignored by the server |
| `ssh.max_auth_tries` | `6` | public-key attempts per connection |
| `ssh.handshake_timeout` | `20s` | banner, key exchange and authentication |
| `ssh.session_timeout` | `30m0s` | hard cap per git operation |
| `ssh.idle_timeout` | `5m0s` | rolling read/write deadline; also `upload-pack --timeout` |
| `git.binary` | `git` | git executable (resolved from `PATH` at start) |
| `git.timeout` | `30s` | bound on read-only plumbing commands used for rendering |
| `git.max_concurrent` | `16` | simultaneous git subprocesses (rendering and transports share the slots; a caller waits up to 10 s then gets "server busy") |
| `limits.max_repo_bytes` | 2 GiB | per-repository size; enforced in pre-receive using the quarantine size (admins exempt) |
| `limits.max_user_bytes` | 10 GiB | per-owner total; enforced at repository creation and in pre-receive (admins exempt) |
| `limits.max_repos_per_user` | `100` | enforced at creation (admins exempt) |
| `limits.max_push_bytes` | 1 GiB | `receive.maxInputSize` for `git receive-pack` |
| `limits.max_titan_bytes` | 64 MiB | largest Titan body on any path; larger `size=` is refused before the body. Must be >= `max_asset_bytes` |
| `limits.max_text_bytes` | 256 KiB | issue, comment and edit bodies |
| `limits.max_asset_bytes` | 64 MiB | one release asset (releases are planned for M3; validated but not yet used) |
| `limits.max_render_bytes` | 512 KiB | blob bytes rendered inline (README, file view); larger files link to `raw` |
| `limits.max_diff_bytes` | 1 MiB | patch text per commit page before truncation |
| `limits.min_free_bytes` | 1 GiB | below this free space: repository creation and pushes are refused and `/status` reports unhealthy (`41`) |
| `limits.max_conns` | `1024` | concurrent Gemini/Titan connections |
| `limits.max_conns_per_ip` | `32` | per source address |
| `limits.ssh_max_conns` | `256` | concurrent SSH connections |
| `limits.ssh_max_conns_per_ip` | `16` | per source address |
| `limits.write_rate_per_minute` | `30` | intended per-account Titan write rate; **not enforced yet** |
| `limits.max_change_bytes` | 64 MiB | one push by a reader proposing a change (M3 change review, in progress) |
| `limits.max_open_changes_per_user` | `10` | open changes per user per repository (M3, in progress) |
| `limits.max_change_commits` | `500` | commits in one change version (M3, in progress) |
| `metrics.listen` | `""` (disabled) | plain-HTTP `/metrics`; bind to a WireGuard or loopback address only |
| `cluster.enabled` | `false` | multi-node mode. Today its only effect is that writes are refused on nodes that are not `repositories.leader_node` |
| `cluster.control_listen` | `""` | node-to-node RPC address; required when enabled (**replication itself is planned, M5**) |
| `cluster.peers` | `{}` | node name -> control address (planned) |
| `cluster.secret_file` | `""` | shared cluster secret (planned) |
| `cluster.sync_interval` | `10s` | replica poll interval (planned) |

Fixed server constants (not configurable): request-line read timeout 10 s,
response write timeout 60 s, Titan body timeout 120 s, request line
1024 bytes (Gemini) / 2048 bytes (Titan), META 1024 bytes, hook RPC
deadline 60 s.

## Directory layout

```
<data_dir>/
  forge.db, forge.db-wal, forge.db-shm   SQLite (WAL, synchronous=NORMAL, foreign keys on)
  repos/<owner>/<repo>.git/              bare repositories (0750 dirs, config owned by the forge)
  assets/<owner>/<repo>/<release>/<name> release assets (planned)
  tmp/                                   in-progress uploads and purge staging (same filesystem as repos)
  tls/server.crt, server.key             service identity (0644 / 0600)
  ssh/host_ed25519, host_ed25519.pub     host key (0600 / 0644)
  hooks/pre-receive, update, post-receive, proc-receive  regenerated at every start: `exec <forge binary> hook <name>`
  hook.sock                              Unix socket (0600) for hook RPC; recreated at start
```

Production nodes keep the identities under `/etc/forge/secrets/` and point
`gemini.cert_file`, `gemini.key_file` and `ssh.host_key_file` there
(`scripts/deploy` renders that configuration).

### Schema (migration `0001_init.sql`)

| Table | Holds |
| --- | --- |
| `users` | accounts: name, display name, bio, `admin`, `disabled` |
| `certificates` | client certificates per user: `spki_sha256` (identity), `cert_sha256`, subject, label, validity, `last_used_at`, `revoked_at` |
| `ssh_keys` | per user: `SHA256:` fingerprint, type, public key, label, `revoked_at` |
| `repositories` | owner, name, description, private, archived, default branch, `vcs`, `leader_node`, `size_bytes`, timestamps, `deleted_at` (soft delete) |
| `collaborators` | (repo, user) -> `read`/`write`/`admin` |
| `issues`, `comments` | tracker; comments target `issue` or `change` |
| `changes`, `reviews`, `releases`, `release_assets` | schema present, features planned (M3) |
| `events` | append-only activity and replication log: kind, repo, user, subject, gemini path, JSON payload, node |
| `tokens` | one-time codes (certificate enrolment), stored hashed with expiry and `used_at` |
| `nodes`, `repo_replicas` | cluster membership and per-replica sync state (M5) |
| `settings` | key/value |
| `schema_migrations` | applied migration versions |

## Admin CLI

`forge admin [--config FILE] <command>`. `--config` defaults to
`FORGE_CONFIG` or `/etc/forge/forge.toml`. Commands open the same database
and repository store as the daemon and may run while it serves.

| Command | Effect |
| --- | --- |
| `init --data DIR --hostname NAME [--node NAME] [--title T] [--write-config FILE] [--gemini-listen A,B] [--ssh-listen A,B] [--ssh-port N] [--gemini-port N]` | create the data directory and database, optionally write the config. Ports for URLs default to those of the first listen address |
| `status` | node, hostname, data dir, schema version, user and repository counts, total repository bytes, free disk, git version |
| `user list` | id, name, admin, disabled, created |
| `user create NAME [--admin]` | create an account (no certificate yet; the user enrols one with `cert enrol-code`) |
| `user disable NAME`, `user enable NAME` | disabled accounts are refused on Gemini/Titan (`61`) and SSH |
| `user admin NAME [--revoke]` | grant or remove administrator |
| `key add USER FILE` | register keys from an `authorized_keys`-style file |
| `key list USER` | fingerprint, type, state, label |
| `key revoke FINGERPRINT` | `SHA256:...` as printed by `key list` |
| `cert list USER` | SPKI hash, state, label, expiry, last use |
| `cert revoke SPKI` | immediate; the next request with that key gets `62` |
| `cert enrol-code USER` | one-time 16-hex code, valid 15 minutes, entered at `/account/enrol` from the new device |
| `repo list` | id, owner/name, private, archived, leader, size, updated |
| `repo create OWNER/NAME [--private] [--description TEXT]` | create as the owner (quota applies) |
| `repo delete OWNER/NAME` | soft delete; restorable for 7 days |
| `repo restore OWNER/NAME` | undo a soft delete within the retention window |
| `repo check OWNER/NAME` | `git fsck --no-dangling`; prints `ok` or the error |
| `repo size OWNER/NAME` | recompute and store on-disk size |
| `maintenance` | purge repositories deleted more than 7 days ago (moved to `tmp/` then removed), delete expired tokens, refresh every repository size |
| `backup --out FILE` | consistent SQLite snapshot via `VACUUM INTO` (see disaster recovery) |

Other subcommands: `forge serve --config FILE`, `forge hook <name>`
(invoked by git only), `forge version`.

## systemd units (`infra/systemd/`)

| Unit | Runs | Notes |
| --- | --- | --- |
| `forge.service` | `forge serve --config /etc/forge/forge.toml` as `forge:forge` | `CAP_NET_BIND_SERVICE` for 22/1965; `ProtectSystem=strict` with `/var/lib/forge` writable; `MemoryMax=600M`, `CPUQuota=90%`, `TasksMax=512`; `Restart=on-failure`; `KillMode=mixed`, 30 s stop timeout (in-flight git sessions are cut at that point) |
| `forge-maintenance.timer/.service` | `forge admin maintenance` nightly at 03:30 +- 45 min, as `forge` | idle IO class, 400 MB cap |
| `forge-backup.timer/.service` | `/usr/local/bin/forge-backup` daily at 04:45 +- 15 min, as root | writes age-encrypted archives under `/var/backups/forge`, keeps 7 |

The unit comments describe `git gc`/`fsck` and SQLite `VACUUM` as part of
maintenance; the current `forge admin maintenance` does only what the table
above says. Repository `gc` is disabled in the git environment
(`gc.auto=0`, `receive.autogc=false`), so repack scheduling is an open item.

`journalctl -u forge -f` follows the log. Reload is a restart
(`systemctl restart forge`); there is no SIGHUP handling.

## Logs

slog, `text` or `json`, one line per event, to stderr. Fields worth
alerting on:

| Message | Fields |
| --- | --- |
| `forge starting` | `version`, `node`, `hostname`, `data` |
| `tls identity` | `created` (true on first generation), `cert` = `CN=... SAN=[...] expires YYYY-MM-DD sha256=<fingerprint>` |
| `ssh host key` | `fingerprint` (`SHA256:...`), `sshfp` (two RDATA strings) |
| `listening` | `proto` (`gemini`, `ssh`, `metrics`), `addr` |
| `request` (`proto=gemini`) | `scheme` (`gemini`/`titan`), `path`, `status`, `bytes`, `ms`, `remote` (IP only), `cert` (bool) |
| `session` (`proto=ssh`) | `remote`, `account`, `key`, `op`, `repo`, `exit`, `ms`, `timeout`, `protocol` |
| `registered` | `user`, `spki` |
| `purged deleted repositories` | `count` |
| errors | `authenticate`, `open repo`, `purge ...`, `event append failed`, `listener failed`, `handler panic` |

Request lines are logged as paths only; query strings (INPUT answers) and
Titan parameters are not logged. Failed SSH authentications are logged at
debug level with the source IP.

## Metrics

`metrics.listen` serves Prometheus text on `/metrics` (plus Go and process
collectors). Gauges marked planned are registered but only populated once
the corresponding feature exists.

| Metric | Type | Labels |
| --- | --- | --- |
| `forge_gemini_requests_total` | counter | `scheme`, `status` (rounded to the decade: `20`, `30`, `50`, ...) |
| `forge_gemini_request_seconds` | histogram | `scheme` |
| `forge_titan_body_bytes_total` | counter | |
| `forge_gemini_connections` | gauge | |
| `forge_ssh_sessions_total` | counter | `op` (`upload`/`receive`), `result` (`ok`/`error`) |
| `forge_ssh_session_seconds` | histogram | `op` |
| `forge_ssh_connections` | gauge | |
| `forge_repositories`, `forge_repository_bytes`, `forge_users`, `forge_disk_free_bytes` | gauge | planned population |
| `forge_replica_lag_events` | gauge | `leader` (M5) |
| `forge_leader_repositories`, `forge_healthy`, `forge_bgp_announced`, `forge_backup_age_seconds` | gauge | M5 / health worker |
| `forge_hook_decisions_total` | counter | `hook`, `decision` (planned) |

## Health: `/status`

`gemini://host/status` (public, no certificate) answers
`20 text/plain` with `ok`, node name, version and uptime, or
`41 unhealthy: disk` when free space is below `limits.min_free_bytes`. It
is what `scripts/deploy smoke` checks. `docs/network-architecture.md`
refers to it as `/healthz`; the implemented path is `/status`, and the
health worker that will drive `bgp-announce` from it is part of M5.

## Maintenance, purge and quotas

- **Deleted repositories** are soft-deleted (`deleted_at`), disappear from
  every page and feed immediately, and stay restorable for 7 days
  (`forge.DeleteRetention`) with `forge admin repo restore`. The daemon's
  hourly loop and `forge admin maintenance` purge older ones: the directory
  is renamed into `tmp/purge-<id>-<name>.git` and removed, then the row is
  deleted (cascading issues, comments, collaborators; events keep a NULL
  repo).
- **Tokens** (enrolment codes) are purged when expired or used.
- **Quotas**: see the `limits.*` rows. Repository creation checks
  `max_repos_per_user`, `max_user_bytes` and free disk; pushes check free
  disk, `max_repo_bytes` and `max_user_bytes` against the size of the
  quarantined pack, and `max_push_bytes` via git. Administrators are exempt
  from count/size quotas but not from the free-disk floor. Sizes are
  refreshed after each push and nightly.
- **Archived repositories** refuse pushes, new issues and comments.
- **Disabled users** are refused everywhere; their content stays.

## Upgrade and rollback

`scripts/deploy <pop>` builds a static linux/amd64 binary, installs it as
`/usr/local/bin/forge` keeping the previous one as `forge.prev`, pushes
units, configs and the node's secret subset, restarts `forge` and runs the
smoke test (`/status` over Gemini, SSH banner). Migrations run at startup;
they are additive, so an older binary can still open a newer database
unless a migration changes columns it reads (check `migrations/` before
rolling back across a schema change).

`scripts/deploy rollback <pop>` swaps `forge.prev` back and restarts. For
an anycast POP wrap the restart in a drain:

```
scripts/deploy drain ewr1        # bgp-announce withdraw on the node
scripts/deploy ewr1              # or: deploy rollback ewr1
scripts/deploy undrain ewr1      # bgp-announce announce
```

`scripts/deploy drain` maps to `bgp-announce withdraw` (immediate). For a
gentler sequence run on the node `bgp-announce drain` (prepend + graceful
shutdown community, traffic moves within the BGP convergence time), wait
about 300 s, then `withdraw`, do the work, then `announce`.
`bgp-announce status` shows the state and exported route counts. A freshly
built node starts withdrawn.

## Certificates and host keys

The service certificate and SSH host key are the same on every POP and are
pinned by clients; rotation is a planned, announced event. Procedures,
what clients see, and compromise handling: `docs/tls.md`.

## Git subprocess environment (security note)

`git.Backend.Env()` is the only environment git ever runs with, for
rendering, transports and hooks:

```
PATH=/usr/local/bin:/usr/bin:/bin   HOME=<data_dir>   LANG=C.UTF-8   LC_ALL=C.UTF-8
GIT_CONFIG_NOSYSTEM=1   GIT_CONFIG_GLOBAL=/dev/null   GIT_TERMINAL_PROMPT=0
GIT_ATTR_NOSYSTEM=1     GIT_ASKPASS=/bin/false
GIT_CONFIG_COUNT/KEY_n/VALUE_n:
  core.hooksPath=<data_dir>/hooks   core.protectNTFS=true   core.protectHFS=true   core.fsmonitor=false
  transfer.fsckObjects=true   receive.fsckObjects=true   fetch.fsckObjects=true
  receive.autogc=false   gc.auto=0   receive.advertisePushOptions=false   receive.denyCurrentBranch=ignore
  uploadpack.allowAnySHA1InWant=false   uploadpack.allowReachableSHA1InWant=false
  uploadpack.allowFilter=true   uploadpack.allowRefInWant=true
  protocol.version=2   protocol.allow=never   protocol.ssh.allow=never   protocol.file.allow=always   protocol.ext.allow=never
  safe.directory=*   advice.detachedHead=false   color.ui=never   i18n.logOutputEncoding=utf-8
  receive.maxInputSize=<limits.max_push_bytes>
```

The SSH server adds `FORGE_ACCOUNT_ID`, `FORGE_ACCOUNT`, `FORGE_REPO`,
`FORGE_HOOK_SOCKET`, `FORGE_OP`, `FORGE_REMOTE_IP` and, if the client asked,
`GIT_PROTOCOL=version=2`. Repository `config` files are written by the
forge at creation and never by users.
