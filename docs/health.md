# Health worker and announcement control

`internal/health` is the watchdog that decides whether this POP should
attract anycast traffic. Every 10 s it runs the local self-checks in
parallel; the verdict of each cycle feeds a small state machine with
hysteresis that drives `scripts/bgp-announce` (and through it BIRD). The
design goal from `docs/threat-model.md` (T-32) is simple: **never flap**. A
POP that is broken at layer 7 must leave quickly, a POP that is merely
noisy must not leave at all, and a POP that keeps coming and going must
stay out longer each time.

## Checks

| Name      | What it does                                                                                     | Configured by                    |
| --------- | ------------------------------------------------------------------------------------------------ | -------------------------------- |
| `gemini`  | TLS connect to the local Gemini listener, request `gemini://<hostname>/status`, expect `20`      | `GeminiAddr`, `Hostname`         |
| `ssh`     | TCP connect to the local SSH listener, read the `SSH-2.0-forge` banner                           | `SSHAddr`                        |
| `disk`    | `statfs` on the data directory, free bytes must be at least `DiskMin`                            | `DataDir`, `DiskMin`             |
| `db`      | Ping the SQLite handle (`store.DB().PingContext`)                                                | a `Pinger` passed by the caller  |
| `replica` | Replication lag (events behind the leader) must not exceed `ReplicaLagMax`                       | `ReplicaLagMax`, a lag function  |

A check whose configuration is empty is skipped. Each check runs under its
own timeout (`CheckTimeout`, 5 s); a check that ignores its context is
abandoned when the timeout fires, and a panicking check counts as a
failure. **A cycle is healthy iff every check passes.**

The Gemini check speaks to the same listener users reach, so it exercises
TLS, the request parser, the handler and (through `/status`) the existing
disk gate. It skips certificate verification: the identity is self-signed
and pinned by clients (TOFU); the probe only asks "does the daemon answer".

`Controller.Healthy()` exposes the last verdict to the `/status` page: a
node reports `41 unhealthy: <failing checks>` while any check fails, and
`41 unhealthy: starting` before its first cycle, so the deploy smoke test
also sees the watchdog's opinion.

## State machine

```
                    N = 3 consecutive failures (30 s)
   ANNOUNCED  ------------------------------------------>  DRAINED
      ^  |          bgp-announce drain                        |   |
      |  | operator: drain / withdraw                         |   | M = 6 consecutive failures
      |  v          (pinned until undrain)                    |   | (60 s total)  bgp-announce withdraw
      |  DRAINED / WITHDRAWN                                  |   v
      |                                                       | WITHDRAWN
      |    K = 6 successes AND cooldown since last change     |   |
      +-------------------------------------------------------+   |
           bgp-announce undrain                                   |
                                                                  |
   WITHDRAWN --(K successes AND cooldown)--> DRAINED --(next healthy tick)--> ANNOUNCED
                bgp-announce announce; drain             bgp-announce undrain
```

States map onto the two constants in `/etc/bird/state.conf`:

| State       | `ANNOUNCE` | `DRAIN` | Meaning                                                              |
| ----------- | ---------- | ------- | -------------------------------------------------------------------- |
| `announced` | true       | false   | normal export                                                        |
| `drained`   | true       | true    | exported with AS-path prepend and the GRACEFUL_SHUTDOWN community    |
| `withdrawn` | false      | false   | nothing exported                                                     |

Counters are per cycle and consecutive: a healthy cycle increments
`successes` and zeroes `failures`; a failed cycle does the reverse.

Transitions:

| From        | To          | Condition                                                             | Script verbs        |
| ----------- | ----------- | --------------------------------------------------------------------- | ------------------- |
| `announced` | `drained`   | `failures >= FailuresToDrain` (3)                                     | `drain`             |
| `announced` | `withdrawn` | `failures >= FailuresToWithdraw` (6) (only if `drain` kept failing)   | `withdraw`          |
| `drained`   | `withdrawn` | `failures >= FailuresToWithdraw` (6)                                  | `withdraw`          |
| `drained`   | `announced` | `successes >= SuccessesToRecover` (6) and cooldown elapsed            | `undrain`           |
| `withdrawn` | `drained`   | `successes >= SuccessesToRecover` (6) and cooldown elapsed            | `announce`, `drain` |
| `drained`   | `announced` | re-entry: the next cycle after the row above is healthy               | `undrain`           |

The cooldown is measured from the last state change. Re-entry from
`withdrawn` passes through `drained` for one tick so the prefix is visible
with a prepend before it becomes anyone's best path; if that tick fails the
fast path is cancelled and the normal `drained -> announced` rule applies.

Why drain before withdraw: a `drain` keeps answering while the prepend and
the GRACEFUL_SHUTDOWN community (RFC 8326) make upstreams prefer other POPs,
so established TCP sessions finish and new ones land elsewhere. A plain
withdraw moves every path at once and resets whatever was in flight.

### Flap backoff

Each health-driven exit from `announced` records the time. If the previous
one was less than `FlapWindow` (1 h) ago, the effective cooldown doubles,
capped at `MaxBackoff`: 120 s, 240 s, 480 s, 900 s, 900 s... After a quiet
hour the next exit starts again at the base `Cooldown`. Operator drains and
the shutdown drain are planned and do not count.

### Startup and shutdown

- **Startup**: the controller starts in `withdrawn` and calls
  `bgp-announce withdraw` once to make BIRD agree (the script is
  idempotent). A previous instance leaves the prefix drained on a graceful
  stop and a crashed one may leave it announced; either way this process
  has not yet proven itself. Announcement follows only after `K` healthy
  cycles **and** the cooldown since start, so a crash-looping daemon never
  announces. With the defaults a restart therefore costs about 130 s out of
  rotation: 120 s cooldown, one drained tick, then `undrain`.
- **Shutdown**: on `SIGTERM` the controller calls `bgp-announce drain`
  (not `withdraw`) with a 5 s budget and returns. Draining first is the
  RFC 8326 sequence: the prepend and community steer new connections away
  while the daemon finishes in-flight requests (`gemini.Server.Shutdown`
  waits up to 30 s), and the restarted process withdraws on start, so the
  prefix is never absent while a node that still answers could serve it.
  For maintenance that outlasts a restart, withdraw explicitly (below).

### Failure of the script itself

If `bgp-announce` fails (birdc unreachable, `configure check` rejects the
file, timeout of 20 s), the transition is logged at error level and the
state is left unchanged; the next cycle re-evaluates the same condition and
retries. Counters keep growing meanwhile, so a node whose `drain` cannot be
applied reaches the withdraw threshold and tries `withdraw` instead.

## Defaults and tuning

| Field                | Default | Notes                                                                  |
| -------------------- | ------- | ---------------------------------------------------------------------- |
| `Interval`           | 10 s    | cycle period; detection latency is `Interval * FailuresToDrain`        |
| `FailuresToDrain`    | 3       | 30 s of consecutive failure before draining                            |
| `FailuresToWithdraw` | 6       | 60 s before withdrawing (clamped to at least `FailuresToDrain`)        |
| `SuccessesToRecover` | 6       | 60 s of consecutive health before re-entry                             |
| `Cooldown`           | 120 s   | minimum time since the last transition before re-entry                 |
| `MaxBackoff`         | 900 s   | ceiling for the doubled cooldown                                       |
| `FlapWindow`         | 1 h     | exits closer together than this double the cooldown                    |
| `CheckTimeout`       | 5 s     | per check                                                              |
| `DiskMin`            | 1 GiB   | should match `limits.min_free_bytes`                                   |
| `ReplicaLagMax`      | 0       | 0 disables the replica check                                           |
| `Announcer`          | ""      | path to `scripts/bgp-announce`; empty means no BGP control (dev, single node) |

Guidance:

- Detection latency is bounded by `Interval * FailuresToDrain`; lowering it
  below 30 s buys speed at the cost of reacting to 10 s blips (a stuck
  `birdc`, a long checkpoint). Prefer raising `FailuresToDrain` over
  raising `Interval`: a longer interval also delays recovery.
- `SuccessesToRecover * Interval` must exceed the longest transient you
  expect from a node that is genuinely recovering (SQLite WAL checkpoint,
  git repack). 60 s is generous for this daemon.
- `Cooldown` is the anti-flap knob. It should be longer than BGP
  convergence to your upstream plus the time a client needs to notice and
  retry, so that a node's second exit is visibly rarer than its first.
  Never set it below `SuccessesToRecover * Interval`.
- Set `Announcer` only on POPs that run BIRD. Everywhere else the state
  machine still runs (useful for `/status` and the metrics) against a
  no-op announcer.

## Operator overrides

`forge admin pop drain` / `forge admin pop undrain` (and `withdraw`) call
`Controller.Drain`, `Controller.Undrain` and `Controller.Withdraw`.

- **drain** drains now if announced and *pins* the state: checks keep
  running and the metrics keep updating, but no transition happens until
  `undrain`. Use it before a deploy or when you want traffic off a node
  while you look at it. Because the drained prefix still carries a path,
  a node left drained for a long time is still a fallback; combine with
  `withdraw` for real maintenance.
- **withdraw** withdraws now and pins. The documented maintenance sequence
  is `drain`, wait about 300 s for paths to move, `withdraw`, do the work,
  `undrain`.
- **undrain** releases the pin. The node re-enters through the normal
  hysteresis: it needs `K` healthy cycles and the cooldown counted from the
  pin, which is usually already satisfied after maintenance, so the next
  cycle triggers `announce` + `drain` and the one after it `undrain`.
  `undrain` without a pin returns `ErrPinned`.

The health worker owns `state.conf` while it runs. If you edit the file or
run `bgp-announce` by hand on a live node, pin the controller first
(`drain`) so it does not fight you, and prefer the admin verbs so its idea
of the state matches BIRD's.

## Interaction with `bgp-announce` and BIRD

`bgp-announce` rewrites `/etc/bird/state.conf` atomically, runs
`birdc configure check`, then `birdc configure`, and rolls the file back if
either fails; it is idempotent (no reload when nothing changes). The
controller calls it with one verb per transition and a 20 s timeout.

Two script rules shape the controller:

- `drain` and `undrain` refuse to act while withdrawn. The controller
  therefore never calls `drain` from `withdrawn`; re-entry is `announce`
  followed by `drain`. The window between the two reloads (one `birdc`
  round-trip) is the only moment a re-entering node is exported without a
  prepend; a future `announce-drained` verb in the script would close it.
- The generated `state.conf` starts withdrawn, matching the controller's
  initial state.

The state file is the source of truth for BIRD across BIRD restarts: a
BIRD that comes back after a crash re-reads `state.conf` and exports what
the controller last decided.

## Metrics

| Metric               | Value                                                                          |
| -------------------- | ------------------------------------------------------------------------------ |
| `forge_healthy`      | 1 when the last cycle passed every check, else 0 (updated every cycle)         |
| `forge_bgp_announced`| 1 only in `announced`; 0 while drained or withdrawn (a drained node still exports but is not meant to carry traffic) |

`Controller.Status()` returns the full picture (state, pin, counters,
per-check results, time of the last change, effective cooldown) for the
admin CLI and the status page.

## Alert rules

Node-level, from the forge metrics:

```yaml
groups:
  - name: forge-health
    rules:
      - alert: ForgeUnhealthy
        expr: forge_healthy == 0
        for: 2m
        labels: {severity: page}
        annotations:
          summary: "{{ $labels.instance }} failing health checks for 2m (check /status detail)"

      - alert: ForgeHealthyButNotAnnounced
        expr: forge_healthy == 1 and forge_bgp_announced == 0
        for: 10m
        labels: {severity: warn}
        annotations:
          summary: "{{ $labels.instance }} healthy but not announcing for 10m: pinned by an operator, in a long flap cooldown, or bgp-announce failing (see daemon log 'bgp transition failed')"

      - alert: ForgeAnnouncedButUnhealthy
        expr: forge_healthy == 0 and forge_bgp_announced == 1
        for: 2m
        labels: {severity: page}
        annotations:
          summary: "{{ $labels.instance }} announcing while unhealthy: drain is not taking effect"

      - alert: ForgeFlapping
        expr: changes(forge_bgp_announced[1h]) >= 4
        labels: {severity: warn}
        annotations:
          summary: "{{ $labels.instance }} changed announcement state {{ $value }} times in 1h"

      - alert: ForgeNoAnnouncers
        expr: sum(forge_bgp_announced) == 0
        for: 1m
        labels: {severity: page}
        annotations:
          summary: "no POP is announcing the anycast prefixes"
```

Node-level, from BIRD (via `bird_exporter` or equivalent):

```yaml
      - alert: BirdBgpSessionDown
        expr: bird_protocol_up{proto="BGP"} == 0
        for: 3m
        labels: {severity: page}
        annotations:
          summary: "{{ $labels.instance }} BGP session {{ $labels.name }} down"

      - alert: BirdExportsNothingWhileAnnounced
        expr: bird_protocol_prefix_export_count{proto="BGP"} == 0 and on(instance) forge_bgp_announced == 1
        for: 5m
        labels: {severity: page}
        annotations:
          summary: "{{ $labels.instance }} thinks it announces but BIRD exports no prefix"
```

`BirdBgpSessionDown` is the one the health worker cannot see: it checks
the daemon, not the routing session. A node with BIRD down and the daemon
healthy serves nothing to the anycast catchment yet reports
`forge_healthy 1`, which is exactly why the external, BGP-side alert must
exist. Pair these with the prefix-visibility monitoring in
`docs/threat-model.md` (T-30: BGPalerter or RIPE RIS on origin, path and
more-specifics).

## Wiring

```go
hc := health.Default()
hc.Announcer = cfg.Health.Announcer
hc.GeminiAddr, hc.SSHAddr = cfg.Gemini.Listen[0], cfg.SSH.Listen[0]
hc.Hostname, hc.DataDir, hc.DiskMin = cfg.Hostname, cfg.DataDir, cfg.Limits.MinFreeBytes
checks := health.BuiltinChecks(hc, health.PingFunc(app.Store.DB().PingContext), nil)
hw := health.New(hc, checks, health.NewAnnouncer(hc.Announcer)).
	SetLogger(log).
	SetMetrics(&health.Metrics{Healthy: reg.Healthy, Announced: reg.Announced})
handler.Health = hw.Healthy

hctx, hcancel := context.WithCancel(ctx)
hwDone := make(chan struct{})
go func() { defer close(hwDone); _ = hw.Run(hctx) }()

// shutdown path, before gsrv.Shutdown:
hcancel()
<-hwDone // Run bounds its own drain to 5 s
```

Cancel the health context and wait for `Run` before closing the listeners
so the drain reaches BIRD while the daemon still answers.

## Privilege boundary on a node

`forge serve` runs sandboxed (`User=forge`, `NoNewPrivileges`, read-only
`/etc`), so it cannot run `bgp-announce` itself. The announcer configured in
`forge.toml` is `/usr/local/bin/forge-bgp-request`, which writes one of
`announce|withdraw|drain|undrain` to `/var/lib/forge/bgp.request` and waits
(up to 20 s) for `/var/lib/forge/bgp.result`. The root-owned
`forge-bgp.path` unit watches that file and runs `forge-bgp-exec`, which
validates the verb, runs `bgp-announce`, and records `ok`/`failed`. A
compromised forge process can therefore only choose among the four verbs;
it cannot write BIRD configuration. `forge-bgp-request status` prints the
last result. Both files are installed by cloud-init and refreshed by
`scripts/deploy sysupdate`.
