# Resync a replica

**Purpose**: bring a stale or broken replica copy of a repository back in
line with its leader.

**Preconditions**: cluster mode; the leader is up and reachable on the
control plane (`cluster.peers` entry, WireGuard up); you are on the
**replica** (the command refuses on the leader: `not the leader`).

**Duration**: seconds to minutes (a full `git fetch` from the leader plus a
metadata snapshot).

**Triggers**: `forge_replica_lag_events{leader}` growing, `pop status` row
`error`, `move-leader` failing with `not fully synced`, stale pages on one
POP, a replica rebuilt from backup.

## Diagnose first

```sh
# replica #
sudo -u forge forge admin pop status                          # REPO LEADER NODE STATUS LAST SYNC DETAIL
journalctl -u forge --since "-30 min" --no-pager | grep -E 'proto=repl|control auth failed|sync'
curl -s http://[<wg address>]:9100/metrics | grep forge_replica_lag_events
sudo wg show                                                  # peer handshake recent?
curl -s -H "Authorization: Bearer $(sudo cat /etc/forge/secrets/cluster.secret)" -H "X-Forge-Node: <this node>" \
  http://[<leader wg address>]:9200/v1/status                 # 200 JSON expected; 401 = secret mismatch, 403 = name not in leader's peers
```

Common `DETAIL` values and fixes:

| Detail / log | Cause | Fix |
| --- | --- | --- |
| `401`/`control auth failed` | cluster secret differs | `rotate-secrets.md` (cluster), redeploy both |
| `403` | this node's name missing from the leader's `cluster.peers` | fix the leader's `forge.toml`, restart it |
| `repository files missing` | leader's directory gone | restore on the leader (`restore-from-backup.md`), then resync |
| connection refused / timeout | WireGuard or nftables | `network-bootstrap.md`; `nft list ruleset | grep 9200` |
| `events at or below cursor` | leader was rebuilt with the same name and a shorter event log | see `rebuild-pop.md` "cursor reset" |

## Resync one repository

```sh
sudo -u forge forge admin repo resync <owner>/<name>
sudo -u forge forge admin pop status | grep '<owner>/<name>'        # STATUS ok, LAST SYNC now
```

`resync` marks the row `resync`, fetches refs
(`+refs/heads/*`, `+refs/tags/*`, prune) and re-applies the whole
metadata snapshot (collaborators, issues, changes, comments, reviews,
releases) from the leader. It is idempotent.

## Resync everything on a node

There is no `resync --all`. **PLANNED**. Loop instead:

```sh
me=$(sudo -u forge forge admin status | awk '/^node:/{print $2}')
sudo -u forge forge admin repo list | awk -v me="$me" 'NR>1 && $5 != me {print $2}' | while read -r r; do
  sudo -u forge forge admin repo resync "$r" || echo "FAILED $r"
done
```

(`$5` is the LEADER column; repositories this node leads are skipped.)
Alternatively restart the daemon: every cycle retries rows in `error`.

## Verify

```sh
sudo -u forge forge admin repo check <owner>/<name>                  # ok
sudo -u forge forge admin pop status | grep -c error                 # 0
git ls-remote ssh://git@<this pop>.nodes.<zone>/<owner>/<name>.git | head   # refs match the leader's
```

## Rollback

None needed: fetches are mirror updates verified by git, cursors only
advance on success, snapshots apply atomically. If a resync made things
worse the leader's data is what it copied; look there.
