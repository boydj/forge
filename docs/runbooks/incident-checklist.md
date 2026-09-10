# Incident checklist

**Purpose**: the first ten minutes. Establish what is broken, stop the
bleeding (take the POP out of anycast if it is harming users), then pick
the specific runbook.

**Preconditions**: laptop with the repository, SSH access on 2200,
`openssl`, `dig`.

**Duration**: 10 min to a diagnosis.

**Triggers**: any alert from `docs/health.md`; a user report; a failed
deploy; you noticed something odd.

## 0. Is the service reachable at all? (laptop, 1 min)

```sh
printf 'gemini://git.<zone>/status\r\n' | timeout 10 openssl s_client -quiet -connect git.<zone>:1965 -servername git.<zone> 2>/dev/null | head -3
#   20 text/plain + "ok <node> <version> <uptime>" = the POP you landed on is fine
#   41 unhealthy: <checks>            = that POP's watchdog failing (disk, gemini, ssh, db); node name tells you which POP
#   nothing / timeout                 = routing or TLS problem, or no POP announcing
for pop in ewr1 <others>; do scripts/deploy smoke $pop; done            # per-node names bypass anycast: which POPs are actually up?
dig +short A git.<zone>; dig +short AAAA git.<zone>                      # records still the anycast addresses?
ssh -T git@git.<zone>                                                    # "forge: interactive shell not available; use git" is healthy
```

## 1. Stop the bleeding (2 min)

A POP that answers wrongly is worse than one that is gone: anycast will
route around an absent POP.

```sh
scripts/deploy drain <pop>                    # bgp-announce withdraw on the node; drain-pop.md
# node unreachable but still announcing: Vultr console -> stop the instance (BIRD dies with it, upstream withdraws)
```

Do not drain the last announcing POP unless it is actively harmful.

## 2. On the node (5 min)

```sh
ssh -p 2200 deploy@<pop>.nodes.<zone>
systemctl status forge bird wg-quick@wg0 nftables --no-pager | grep -E 'Active|●'
journalctl -u forge -n 200 --no-pager                                    # look for: listener failed, handler panic, event append failed,
                                                                         #   bgp transition failed, control auth failed, tls identity, ssh host key
journalctl -u forge --since "-1 hour" -p warning --no-pager | tail -50
sudo -u forge forge admin status                                         # schema, counts, disk free, git version
sudo -u forge forge admin pop status                                     # drain pin? replica rows in cluster mode
sudo bgp-announce status                                                 # announced / draining / withdrawn, exported routes
sudo birdc show protocols all | grep -E '^[a-z]|BGP state|Routes:'       # Established? routes imported/exported?
sudo wg show                                                             # mesh peers: latest handshake < 3 min
curl -s http://127.0.0.1:9100/metrics 2>/dev/null | grep -E '^forge_(healthy|bgp_announced|gemini_connections|ssh_connections|replica_lag)'   # use the wg0 address in a mesh
df -h / /var/lib/forge; free -m; uptime
ss -ltnp | grep -E ':(22|1965|2200|9100|9200) '                          # who owns the ports
sudo nft list ruleset | grep -cE 'dport \{ 22, 1965 \}'                  # firewall rules present (2 expected: v4 + v6)
```

## 3. Decide

| Finding | Go to |
| --- | --- |
| `/status` 41 with `disk`; `df` near full | `disk-full.md` |
| daemon not running, restart loop, migration/config error in the journal | `upgrade-and-rollback.md` (rollback); db error: `restore-from-backup.md` |
| daemon healthy, `bgp-announce status` withdrawn/draining, `pop status` says pinned | someone left it drained: `drain-pop.md` re-entry |
| daemon healthy, BIRD not Established | `network-bootstrap.md` (session, MD5, provider addresses in bird.conf) |
| all POPs fine on their own names, anycast unreachable | upstream/RPKI/prefix problem: `network-bootstrap.md`, provider portal; check RIPE RIS / BGPalerter for the prefix |
| `control auth failed`, replica rows `error`, lag growing | `resync-replica.md` |
| pushes refused "not the leader; leader is X", X down | `move-leader.md` / `rebuild-pop.md` |
| TLS fingerprint or host key in the journal differs from the published one | possible tampering or a generated identity: withdraw the POP, `rotate-tls-certificate.md` "compromise", `rebuild-pop.md` |
| suspicious sessions, spam, abuse | `revoke-user-credentials.md`, `handle-abuse-report.md` |
| node unreachable on 2200, console dead | `rebuild-pop.md` |
| `PRAGMA integrity_check` not ok | `restore-from-backup.md` (database corruption) |

```sh
sudo -u forge sqlite3 /var/lib/forge/forge.db 'PRAGMA integrity_check;'    # when in doubt about the database
```

## 4. Communicate and record

- Status line for users: what is affected (reads, writes, one POP), since
  when, ETA. Post it on the front page if the forge is up, otherwise in
  the project repository / other channel.
- Operations log: time, symptom, what you ran, what fixed it, follow-ups.
  Include `forge version`, `journalctl` excerpts and the archive name if
  a restore happened.
- After: undo any drain (`drain-pop.md`), confirm `forge-backup.timer`
  and `forge-maintenance.timer` are active, and open follow-up items for
  every PLANNED gap that hurt.

## Verify the all-clear

```sh
for pop in ewr1 <others>; do scripts/deploy smoke $pop; done
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect git.<zone>:1965 -servername git.<zone> 2>/dev/null | head -2
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo bgp-announce status; sudo -u forge forge admin pop status | head -3'
```
