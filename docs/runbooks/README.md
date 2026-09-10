# Runbooks

Copy-pasteable procedures for operating forge nodes. Each runbook states its
purpose, preconditions, exact commands, verification, rollback, expected
duration and the alerts that trigger it. Background lives in `docs/`
(`operations.md`, `self-hosting.md`, `secrets.md`, `tls.md`,
`disaster-recovery.md`, `replication.md`, `health.md`, `git-ssh.md`).

Steps marked **PLANNED** depend on tooling that does not exist yet; the
runbook says what to do by hand meanwhile. Never invent a command: if a
step is PLANNED, do the manual alternative shown.

## Conventions

| Symbol | Meaning |
| --- | --- |
| `<pop>` | node name from `infra/network/address-plan.yaml`, e.g. `ewr1` |
| `<zone>` | DNS zone, default `as215520.net` (`FORGE_ZONE`) |
| laptop `$` | run from the repository root on the operator machine (`scripts/*`, `tofu`) |
| node `#` | run on the node as root or via `sudo` after `ssh -p 2200 deploy@<pop>.nodes.<zone>` |
| `forge admin` | on a node: `sudo -u forge forge admin <cmd>` (config `/etc/forge/forge.toml`); may run while the daemon serves unless stated |

The `sqlite3` CLI used by some audit and repair steps is **not** installed
by cloud-init or `deploy bootstrap`: `sudo apt-get install -y sqlite3`
once per node before those steps on nodes bootstrapped before 2026-09-10; cloud-init now installs it.

Public ports: 1965 (Gemini/Titan), 22 (git over SSH). Operator SSH: 2200.
Metrics: `http://[<wg address>]:9100/metrics` (loopback on a single node),
node exporter on 9101, cluster RPC on 9200, all reachable from wg0 only.

## Index

| Runbook | Use when |
| --- | --- |
| [incident-checklist.md](incident-checklist.md) | anything is wrong and you do not know what yet |
| [deploy-first-node.md](deploy-first-node.md) | bringing up a new environment or the first node from zero |
| [network-bootstrap.md](network-bootstrap.md) | enabling BGP, WireGuard mesh and anycast for a POP |
| [upgrade-and-rollback.md](upgrade-and-rollback.md) | shipping a new binary or config; reverting one |
| [drain-pop.md](drain-pop.md) | taking a POP out of (or back into) anycast rotation |
| [move-leader.md](move-leader.md) | moving write ownership of a repository to another node |
| [resync-replica.md](resync-replica.md) | a replica is stale, `error` or lagging |
| [rebuild-pop.md](rebuild-pop.md) | a node is lost, compromised or must be recreated |
| [restore-from-backup.md](restore-from-backup.md) | restoring data on one node or after total loss |
| [rotate-tls-certificate.md](rotate-tls-certificate.md) | changing the service certificate (TOFU impact) |
| [rotate-ssh-host-key.md](rotate-ssh-host-key.md) | changing the SSH host key (SSHFP) |
| [rotate-secrets.md](rotate-secrets.md) | age recipients, WireGuard, cluster secret, provider tokens, backup key |
| [revoke-user-credentials.md](revoke-user-credentials.md) | a user's certificate or SSH key is compromised, or an account must be disabled |
| [handle-abuse-report.md](handle-abuse-report.md) | a takedown or abuse report names content on the forge |
| [disk-full.md](disk-full.md) | `/status` says `41 unhealthy: disk`, pushes refused |

## Alert to runbook map

Alert rules are proposed in `docs/health.md` ("Alert rules"); no alerting
stack is deployed yet (`infra/monitoring/` is empty), so today these are
what you would see by hand.

| Alert / symptom | Runbook |
| --- | --- |
| `ForgeUnhealthy`, `/status` returns `41` | incident-checklist, then disk-full or restore-from-backup |
| `ForgeHealthyButNotAnnounced` | drain-pop (was it pinned?), incident-checklist |
| `ForgeAnnouncedButUnhealthy`, `ForgeFlapping` | drain-pop (pin it), incident-checklist |
| `ForgeNoAnnouncers`, `BirdBgpSessionDown` | incident-checklist, network-bootstrap |
| `forge_replica_lag_events` > 0 for long, `pop status` shows `error` | resync-replica |
| leader node down, pushes refused with "leader is ..." | move-leader, rebuild-pop |
| backup age > 2 days (`ls /var/backups/forge`) | restore-from-backup (verify), incident-checklist |
| user reports "certificate changed" / host key warning | rotate-tls-certificate, rotate-ssh-host-key (was it planned?) |
