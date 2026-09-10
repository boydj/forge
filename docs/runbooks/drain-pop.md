# Drain a POP (and put it back)

**Purpose**: stop an anycast POP from attracting traffic before
maintenance, and re-enter rotation afterwards, without flapping.

**Preconditions**: at least one other POP is announcing; BIRD and
`/usr/local/bin/bgp-announce` are installed on the node (`scripts/deploy`
pushes the script; `network-bootstrap.md` sets up BIRD).

**Duration**: drain ~5 min (BGP convergence), withdraw immediate, re-entry
~130 s after undrain (6 healthy checks + 120 s cooldown).

**Triggers**: planned maintenance, `upgrade-and-rollback.md`,
`rebuild-pop.md`, `ForgeAnnouncedButUnhealthy`, `ForgeFlapping`, key
compromise (`docs/tls.md`).

## Two mechanisms, know which you are using

| Command | Where | What it does |
| --- | --- | --- |
| `forge admin pop drain` | node | writes `<data_dir>/drain` (`config.DrainFile`, `/var/lib/forge/drain`). The daemon polls every 2 s, calls `Controller.Drain`: drains now if announced and **pins** the state; health checks keep running but no automatic transition happens until `pop undrain` removes the file. Survives daemon restarts (file stays). |
| `forge admin pop undrain` | node | removes the file; the controller releases the pin and re-enters through the normal hysteresis (K=6 healthy cycles and cooldown, then `announce`+`drain`, then `undrain`). |
| `forge admin pop status` | node | prints `drain: pinned by operator` when the file exists, plus replica rows in cluster mode |
| `bgp-announce drain\|undrain\|withdraw\|announce\|status` | node, root | rewrites `/etc/bird/state.conf` and reloads BIRD directly. `drain` = keep exporting with AS-path prepend + GRACEFUL_SHUTDOWN; `withdraw` = export nothing. `drain`/`undrain` refuse while withdrawn. |
| `scripts/deploy drain <pop>` / `undrain <pop>` | laptop | runs `bgp-announce withdraw` / `announce` on the node. Note: **withdraw**, not the gentle drain. |

The health controller owns `state.conf` while the daemon runs with
`health.announcer` set. If you run `bgp-announce` by hand on such a node,
pin first (`pop drain`) so the controller does not fight you. **Today the
rendered `forge.toml` has no `[health]` section** (see
`deploy-first-node.md`), so on deployed nodes the controller is a no-op
and `bgp-announce` is the only thing that moves BIRD; the pin still
matters for `/status`/metrics and for the day the announcer is wired.

There is no `forge admin pop withdraw` (mentioned in `docs/health.md`);
use `bgp-announce withdraw`. **PLANNED**.

## Drain for maintenance (gentle, RFC 8326)

```sh
# node #
sudo -u forge forge admin pop drain           # pin
sudo bgp-announce drain                       # prepend + community; still answering
sudo bgp-announce status                      # state: draining
sleep 300                                     # paths move to other POPs
sudo bgp-announce withdraw                    # now export nothing
sudo bgp-announce status                      # state: withdrawn, "exported via ...: 0 route(s)"
# ... do the work ...
```

Or from the laptop when you do not need the gentle step:

```sh
scripts/deploy drain <pop>                    # = bgp-announce withdraw
```

## Re-enter rotation

```sh
# node #
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername git.<zone> 2>/dev/null | head -1   # must be "20 ..."
sudo bgp-announce announce                    # export again (with the announcer wired, the controller does announce+drain then undrain itself)
sudo -u forge forge admin pop undrain         # release the pin
sudo bgp-announce status
```

Or `scripts/deploy undrain <pop>` then `pop undrain` on the node.

## What the health controller does on its own (when wired)

- 3 consecutive failed cycles (30 s): `drain`. 6 (60 s): `withdraw`.
- Recovery: 6 consecutive healthy cycles and the cooldown (120 s, doubling
  up to 900 s if the last exit was < 1 h ago), then `announce` + `drain`,
  then `undrain` on the next healthy tick.
- Start: withdrawn, `bgp-announce withdraw` once; announces after ~130 s.
- SIGTERM: `bgp-announce drain` (not withdraw) with a 5 s budget.
- Operator pins (`pop drain`) and the shutdown drain do not count as flaps.

Checks: `gemini` (TLS to the local listener, `/status` must be `20`),
`ssh` banner, `disk` (>= `limits.min_free_bytes`), `db` ping. The
`replica` lag check is registered but `serve` passes no lag function, so
it is skipped. **PLANNED**.

## When to withdraw rather than drain

- Node will restart the daemon or reboot: withdraw first (the drained
  prefix still carries a path; a rebooting node blackholes it).
- Node is compromised or its keys are suspect: withdraw immediately, or
  from the provider portal if you no longer trust the host.
- BIRD itself is broken (`birdc` unreachable): stop BIRD
  (`systemctl stop bird`); the session drops and upstream withdraws.
- Leave a POP merely *drained* only for short observation windows; it
  remains a fallback path.

## Verify

```sh
sudo bgp-announce status                          # state and exported route count
sudo birdc show protocols all | grep -E 'BGP|Routes:'  # session Established; exported 0 when withdrawn
curl -s http://127.0.0.1:9100/metrics | grep -E '^forge_(healthy|bgp_announced)'   # 9100 on the wg0 address in a mesh
sudo -u forge forge admin pop status
```

From outside: `traceroute git.<zone>` from a vantage point that used to
land on this POP; the drained POP's per-node name still answers.

## Rollback

`bgp-announce announce` then `pop undrain`; both are idempotent. If
`bgp-announce` reports `configuration check failed` it has already rolled
`state.conf` back; fix `bird.conf` (`bird -p -c /etc/bird/bird.conf`)
and retry.
