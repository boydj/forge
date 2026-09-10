# Failure testing and load testing

What breaks, what forge is expected to do about it, and how that is
verified. The automated part lives in `tests/failure_test.go` (scenarios
1-11) and `tests/load_test.go`; the load generator is `tools/loadtest`.
Everything runs against the built binary on loopback, so a laptop can run
the whole file in about 40 s. Scenarios that need real network gear (BGP,
WireGuard) are described at the end with the manual procedure.

## Running

```
make build
go test -count=1 -tags integration -run TestFailure ./tests/          # all scenarios, ~32 s
go test -count=1 -tags integration -run 'TestFailure/LeaderLoss' -v ./tests/
go test -count=1 -tags integration -run TestLoadSmoke -v ./tests/      # ~10 s, prints numbers
go run ./tools/loadtest -url gemini://localhost:1965/~alice/proj/ -c 32 -d 10s
```

Each scenario starts its own node(s) in a temp directory (`startNode`,
`startClusterNode`), so they are independent and can be run alone. On a
failure the daemon log of every node involved is printed. Subtests that end
in `SKIP` are known product gaps, described in the skip message and in the
"Findings" section below; they turn into failures once the gap is closed
and the skip removed.

## Scenarios

| # | Subtest | Injection | Expected | Observed (this run) |
| --- | --- | --- | --- | --- |
| 1 | `LeaderLoss` | two-node cluster, `SIGKILL` the leader `a` | reads on `b` keep working (overview, blob, feed); a Titan write on `b` fails fast with `40 writes are not accepted on this node right now`; `git push` to `b` is refused naming node `a`; after `a` restarts on the same data dir pushes work again and `b` catches up; forwarded writes work again | PASS; the Titan failure arrived in 2-4 ms (loopback `ECONNREFUSED` on the control plane, no hang) |
| 2 | `ReplicaLoss` | `SIGKILL` the replica `b` | `a` serves reads and accepts a push (the post-push `notify` to `b` is best effort); after restart `b` converges within the sync interval and refs are identical | PASS, converged well under the 15 s bound with `sync_interval = 500ms` |
| 3 | `InterruptedReplication` | push a 4 MiB random blob to `a` while `b` is down, start `b`, `SIGKILL` it 300 ms later, restart | refs converge, `forge admin repo check` passes on `b`, tree page renders | PASS; in most runs the kill lands before the first fetch finished (replica ref still empty), the retry completes it; no stale ref locks were left behind |
| 4 | `StaleReplica` | as 1, then inspect `forge admin pop status` on `b` | staleness observable: status `error` with a detail, or lag | SKIP (gap): `b`'s log records `sync cycle finished with errors ... connection refused` within one interval, and reads continue, but `pop status` keeps `ok` with a frozen `LAST SYNC`; see F1 |
| 5 | `DiskFull` | restart a seeded node with `limits.min_free_bytes = 1<<60` so `CheckDisk` always fails | reads answer `20`; `/status` answers `41 unhealthy: ...`; push refused with `server is low on disk space`; release asset upload answers `41 insufficient storage` and nothing is stored | PASS (`/status` reports `41`, first from `starting`, then from the `disk` check) |
| 6 | `OversizePush` | `limits.max_push_bytes = 1 MiB`, push a 3 MiB blob | push fails with git's `maximum allowed size` / `unpack failed`; `refs/heads/main` unchanged; overview does not show the commit | PASS |
| 7 | `RepoCorruption` | zero 64 bytes in the middle of every object file of a pushed repository | `forge admin maintenance --check` exits non-zero and prints `corrupt: 1`; `repo check` fails; repository pages answer 4x/5x; `/status` and `/` still answer | PASS for maintenance/check/daemon-alive; `PagesDegrade` SKIP (gap F2): blob and log answer `51`, refs `40`, but the overview answers `20` with an empty body |
| 8 | `MetadataFailure` | stop the node, overwrite the first 100 bytes of `forge.db` with `0xff`, start | `forge serve` exits 1 within seconds with `open database: file is not a database`; no listener bound | PASS (fails in ~50 ms) |
| 8 | `MetadataFailure/AdminRestore` | `forge admin restore --in backup.tar.gz` from a backup taken before the corruption | repository, issue and SSH key are back | SKIP (bug F3): the restored database is empty |
| 8 | `MetadataFailure/ManualRestore` | the procedure from `docs/disaster-recovery.md`: copy `forge.db` from the archive, `git init --bare` + `fetch` each bundle, set `HEAD` | overview shows the commit, issue #1 is readable, `repo check` ok, a new push succeeds | PASS |
| 9 | `ConnectionLimits/Gemini` | `limits.max_conns_per_ip = 4`; hold 4 idle TLS connections, open 4 more | the extra 4 are closed without a response (TLS handshake or first read fails); the 4 held ones still get answers; after closing them a new request works | PASS |
| 9 | `ConnectionLimits/SSH` | `limits.ssh_max_conns_per_ip = 2`; hold 2 raw TCP connections after reading the banner, open a third | the third gets no `SSH-2.0-forge` banner (closed); after releasing, a new connection gets the banner | PASS |
| 10 | `Slowloris` | TLS handshake, then send nothing | the server closes the connection at the read timeout (10 s, not configurable); it sends `59 malformed request` first, which is fine | PASS, closed after 10.00 s |
| 11 | `IPv6` | node with `--gemini-listen "[::1]:P" --ssh-listen "[::1]:P"` | clone and push over `ssh://git@[::1]:P/...`, Gemini over `[::1]`, nothing bound on `127.0.0.1` | PASS; skips when `::1` is unavailable |

Run time of the whole `TestFailure` on a 4-core laptop: 30-38 s (10 s of
that is the Slowloris wait, 7.5 s the stale-replica observation window).

## Load test

`tools/loadtest` is a small Go client:

- **Request mode** (`-url gemini://...` or `titan://...`): `-c` workers each
  open one TLS connection per request (as Gemini requires), send the
  request, read the header and drain the body, for `-d` seconds or `-n`
  requests. Titan mode adds `;size=;mime=` from `-body`/`-body-file`/`-mime`
  and uses `-cert`/`-key`. Reports requests/s, p50/p95/p99/max/mean latency,
  a histogram of status codes and a histogram of transport errors
  (`dial:refused`, `read-header:eof`, `dial:timeout`, ...). Exit code 3
  when any transport error occurred.
- **Clone mode** (`-clone ssh://git@host:port/o/r.git -n 16 -c 4`): runs
  `git clone --bare` into a temp dir with `-c` concurrent clones; honours
  `GIT_SSH_COMMAND` from the environment.
- `-json` prints the summary as JSON (what `tests/load_test.go` parses).

Note for Titan mode: `limits.write_rate_per_minute` (30) applies per
account, so sustained comment posting from one certificate answers `44`
after the first 30; that is the rate limiter working, not a failure.

`TestLoadSmoke` seeds a repository (6 commits, a README, five 2 KB files)
and runs, on one node with `max_conns_per_ip = 256` (32 clients reconnecting
at full speed race the default limit of 32 because the server releases the
slot after the client already saw the close):

| Target | Load | Assertion | Observed (4 cores, loopback) |
| --- | --- | --- | --- |
| `/~alice/proj/tree/main/` (one `git ls-tree`) | 32 conns x 5 s | no 4x/5x, no transport errors, p99 < 500 ms | 1392 requests, 276 req/s, p50 110 ms, p95 194 ms, p99 246 ms, max 335 ms, all `20` |
| `/~alice/proj/` overview (`git log` + tree + README `cat-file`) | 32 conns x 3 s | no 4x/5x, no transport errors (latency reported only) | 467 requests, 150 req/s, p50 203 ms, p95 298 ms, p99 352 ms, max 440 ms; an earlier run on a busier machine gave p99 579 ms |
| `git clone --bare` over SSH | 4 concurrent, 8 total | 8 ok | 130 ms median, 146 ms max |

Latency here is dominated by git subprocess spawning behind the
`git.max_concurrent = 16` semaphore and the TLS handshake per request
(no session resumption); 32 clients on 4 cores queue on both. Numbers scale
with cores; the assertion threshold is deliberately loose (p99 < 500 ms on
the cheap page) so the smoke test is a regression guard, not a benchmark.

## Findings (product bugs and gaps observed by these tests)

Reported, not fixed; the tests skip the affected check with the same text.

- **F1: a dead leader is invisible in `pop status`.** `internal/repl/sync.go:112-124`
  (`syncPeer`) returns as soon as `GET /v1/status` fails, before any
  `repo_replicas` row is touched, and `syncRepoFrom` (sync.go:254) returns
  early without writing the row when nothing changed. So `pop status`
  shows `ok` with the `LAST SYNC` of the last change both when the cluster
  is idle and when the leader has been down for an hour; the
  `forge_replica_lag_events` gauge (sync.go:134) freezes too. Proposed fix:
  in `syncPeer`, on a status/repos/events failure, mark every replica row
  of repositories led by that peer `error` with detail `peer unreachable:
  <err>` (and back to `ok` on the next success), and have `pop status`
  print the `nodes.last_seen` column so "last contact" is separate from
  "last change".
- **F2: overview of a corrupt repository answers `20` with an empty body.**
  `internal/web/handler.go:161-162` (`request.page`) writes the `20`
  header before the handler runs any git command; when `repo.Empty`
  fails on corrupt objects, `req.fail` at `repo.go:190` cannot replace the
  header (`gemini: header already written`) and the client receives a
  success with no content. Proposed fix: have `page()` only build the
  `*gemini.Page` and write the header in `send()`, or give
  `responseWriter` a way to replace a header while nothing has been
  flushed (the 32 KiB buffer already exists).
- **F3: `forge admin restore` produces an empty database.** `cmd/forge/admin.go:64`
  opens the store (creating a fresh database whose schema sits in the WAL)
  before `adminRestore` runs; `internal/forge/backup.go:143-166` then
  extracts the archive's `forge.db` over that file while the connection is
  open, and the checkpoint on `Store.Close()` (backup.go:168) writes the
  fresh, empty pages back over the restored content. Verified by hand:
  1 user/1 repository before, 0/0 after `restore`. Proposed fix: close the
  store *before* extracting (`f.Store.Close()` first, then delete
  `-wal`/`-shm`, then write `forge.db`, then reopen), or extract to
  `forge.db.restore` and `rename` it into place after `Close`. Until then,
  restore by hand as in `docs/disaster-recovery.md` (the
  `ManualRestore` subtest is that procedure).
- **F4 (minor): `forge admin init` mis-parses IPv6 listen addresses.**
  `cmd/forge/admin.go:124-130` uses `strings.Cut(listen, ":")` to derive
  `ssh.port`/`gemini.port`, so `[::1]:2222` yields `port = 22` and clone
  URLs on the pages show the wrong port. Use `net.SplitHostPort`. Serving
  on `[::1]` works (scenario 11).
- Observation, not a bug: on read timeout the Gemini server answers
  `59 malformed request` before closing (`internal/gemini/server.go:224-232`).
  Harmless, but a `59` is logged for every slowloris probe.

## Not testable locally

These need real POPs, BIRD and the WireGuard mesh. Use two dev nodes
deployed with `scripts/deploy` (or the `scripts/dev-cluster` layout on two
hosts with the control addresses moved to the WireGuard interface) and
inject faults with `iptables`/`nft`; watch `forge admin pop status`,
`/status`, the daemon log and the metrics.

**Health controller reference** (`docs/health.md`): checks every 10 s;
3 consecutive failures -> `drain` (prepend + GRACEFUL_SHUTDOWN),
6 -> `withdraw`; recovery needs 6 consecutive successes and the 120 s
cooldown (doubling after a flap inside 1 h, capped at 900 s), passing
through `drained` for one tick.

1. **BGP session loss (upstream down, daemon healthy).** On the POP:
   `iptables -I INPUT -p tcp --dport 179 -j DROP` (and `OUTPUT` to the
   peer's 179). Expected: BIRD's session drops after its hold time; the
   prefix disappears from the upstream; the health worker keeps reporting
   `forge_healthy 1` and `/status 20` because it checks the daemon, not
   the session. Detection is external: `BirdBgpSessionDown` and the
   prefix-visibility monitoring (`docs/health.md`, alert rules). Traffic
   moves to the other POPs by routing alone. Remove the rule: session
   re-establishes, prefix re-announced by BIRD without a forge transition
   (the controller state is unchanged).
2. **Daemon failure with BGP up (the case the controller owns).**
   `iptables -I INPUT -p tcp --dport 1965 -j DROP` on the POP, or `kill
   -STOP` the daemon. Expected: the `gemini` check fails; after 30 s
   `bgp-announce drain` (state `drained`, `forge_bgp_announced 0`), after
   60 s `withdraw`; `/status` answers `41 unhealthy: gemini` while the
   listener is reachable locally (use `kill -STOP` to make it fail the
   probe too). Undo: after 60 s of passing checks plus the cooldown the
   node re-enters via `announce` + `drain` and then `undrain` (about 130 s
   after recovery; `ForgeHealthyButNotAnnounced` must not fire before
   10 min).
3. **WireGuard partition (control plane only).** On POP `b`:
   `iptables -I INPUT -i wg0 -s <a's wg addr> -j DROP` (and `OUTPUT`).
   Expected: public service on both POPs unaffected; on `b` the sync loop
   logs `peer a: ... i/o timeout` every interval (the control client has
   a 2 min HTTP timeout, so the first failure takes up to 2 min, then one
   per interval); Titan writes for repositories led by `a` fail on `b`
   with `40 writes are not accepted on this node right now` after that
   timeout (this is the one place a client can wait up to 2 min: consider
   a shorter per-request timeout for `/v1/forward`); pushes to `b` are
   refused naming `a`. With `health.replica_lag_max` set, `b` drains once
   the lag exceeds it; note the gauge freezes on connection failure
   (F1), so today only a lagging-but-reachable leader triggers this.
   Remove the rule: the next cycle converges, `repo_replicas` returns to
   `ok`.
4. **Asymmetric partition / half-open.** Drop only one direction
   (`INPUT` on `b`, nothing on `a`). Expected: `a`'s `notify` to `b` fails
   (best effort, logged at debug), `b`'s polls time out; behaviour as in 3.
5. **Full POP loss under anycast.** `scripts/deploy drain <pop>` then
   power off, or `ip link set <uplink> down`. Expected: BIRD dies with
   the host and the upstream withdraws the prefix on hold-timer expiry;
   in-flight TLS connections to that POP are reset; clients retry and
   land on the surviving POP. Leadership of repositories led by the dead
   POP stays there until `forge admin repo move-leader` on the old leader
   (which needs it back) - automatic failover is not in v1
   (`docs/replication.md`).

Record the outcome of each manual run (date, POP, what was observed,
time to drain/withdraw/recover) in the operations log next to the restore
drill entries.
