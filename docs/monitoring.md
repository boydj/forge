# Monitoring

Prometheus scrapes every POP over the WireGuard mesh, evaluates the alert
rules in `infra/monitoring/prometheus/rules/forge.yml`, and Grafana renders
`infra/monitoring/grafana/forge-overview.json`. It runs on a dedicated host,
`mon1` (`roles: [monitor]` in `infra/network/address-plan.yaml`), created
by `modules/vultr-monitor`, rendered by `modules/monitor-node` and
configured by `scripts/deploy monitor`; the step-by-step procedure is
`docs/runbooks/deploy-monitor.md`. Everything under `infra/monitoring/` is
the source; the only generated artefact is `prometheus.yml`, rendered by
OpenTofu from the POP list (`tofu output monitor_configs`).

| File | Purpose |
| --- | --- |
| `infra/monitoring/prometheus/prometheus.yml.tftpl` | Prometheus configuration, `templatefile()` input (modules/monitor-node) |
| `infra/monitoring/prometheus/rules/forge.yml` | alert rules (29) |
| `infra/monitoring/prometheus/rules/forge_test.yml` | `promtool test rules` unit tests (run by tests/infra when promtool is installed) |
| `infra/monitoring/blackbox.yml` | blackbox_exporter modules (Gemini and SSH over v4 and v6) |
| `infra/monitoring/bird-exporter.service` | systemd unit for `bird_exporter` on each POP (cloud-init and `deploy sysupdate`) |
| `infra/monitoring/grafana/forge-overview.json` | the dashboard |
| `infra/monitoring/grafana/apt-repo.conf` | the upstream Grafana apt repository and its signing-key fingerprint (pinned) |
| `infra/monitoring/grafana/grafana-server.override.conf` | systemd drop-in: Grafana on loopback, no sign-up, no analytics |
| `infra/monitoring/grafana/provisioning/` | datasource (local Prometheus) and dashboard provider for the host |
| `infra/cloud-init/monitor.yaml.tftpl`, `infra/firewall/nftables-monitor.conf.tftpl` | the host's first boot and firewall |
| `infra/opentofu/modules/vultr-monitor`, `modules/monitor-node`, `environments/dev/monitor.tf` | the instance, the rendering, the wiring |
| `infra/monitoring/dev/` | `docker compose` stack against `scripts/dev-cluster` |

## What is scraped where

All node-side listeners are private: `forge` binds `/metrics` to the wg0
address, and nftables admits `PRIVATE_TCP` only on wg0
(`infra/firewall/nftables.conf.tftpl`). The monitoring host must therefore be
a WireGuard mesh member. The blackbox probes are the exception: they go out
the monitoring host's public interface on purpose.

| Job | Target per POP | Port | Exporter | Notes |
| --- | --- | --- | --- | --- |
| `forge` | `[<wg_address>]:9100` | 9100 | the daemon (`internal/metrics`) | `forge.toml` `[metrics] listen`, rendered by `modules/forge-node` |
| `node` | `[<wg_address>]:9101` | 9101 | `prometheus-node-exporter` (Debian) | collectors: cpu, meminfo, filesystem, netdev, loadavg, diskstats, systemd (`infra/cloud-init/node.yaml.tftpl`) |
| `bird` | `[<wg_address>]:9324` | 9324 | `prometheus-bird-exporter` (Debian trixie 1.4.2+ds-2, upstream `github.com/czerwonk/bird_exporter`) | unit below; 9324 is in `PRIVATE_TCP` (modules/forge-node) |
| `blackbox_*` | public addresses | 1965, 22 | `prometheus-blackbox-exporter` (Debian trixie 0.26.0-1) on the monitoring host, `127.0.0.1:9115` | see Reachability |
| `prometheus`, `blackbox_exporter` | monitoring host | 9090, 9115 | self | |

Every target carries `pop` and `node` labels (today identical: one node per
POP; `node` is the name the daemon knows itself by, `pop` the region). Rules
and the dashboard aggregate on those, never on `instance`. The Prometheus
server's own `external_labels` are `monitor=forge`, `pop`, `node` set to the
monitoring host's identity; Prometheus only adds external labels to an alert
when the alert does not already carry them, so per-POP alerts keep the POP's
values and only fleet-wide alerts (`ForgeAllPopsWithdrawn`) say `pop=mon1`.

Scrape and evaluation interval are 30 s. The health worker cycles every
10 s and drains after 30 s of failure (`docs/health.md`), so a 30 s scrape
sees every state the controller can settle into; nothing here needs to be
faster than the thing it watches.

### The forge metrics

From `internal/metrics/metrics.go` (also listed in `docs/operations.md`):

| Metric | Labels | Used by |
| --- | --- | --- |
| `forge_gemini_requests_total` (counter) | `scheme` (gemini/titan), `status` rounded to the decade (`20`, `30`, `40`, `50`, `60`) | `GeminiErrorRate`, service row |
| `forge_gemini_request_seconds` (histogram) | `scheme` | `GeminiLatencyHigh`, latency panel |
| `forge_titan_body_bytes_total` (counter) | | `TitanBodyBytesSpike` |
| `forge_gemini_connections`, `forge_ssh_connections` (gauge) | | `GeminiConnectionsHigh`, connections panel |
| `forge_ssh_sessions_total` (counter) | `op` (upload/receive), `result` (ok/error) | `SSHSessionErrors` |
| `forge_ssh_session_seconds` (histogram) | `op` | git row |
| `forge_ssh_push_forwards_total` (counter) | `role` (replica/leader), `result` (ok/rejected/unreachable) | not alerted yet; `unreachable` on a replica means its leader was down during a push |
| `forge_repositories`, `forge_repository_bytes`, `forge_users` (gauge) | | storage row |
| `forge_disk_free_bytes` (gauge) | | `DiskLow` |
| `forge_replica_lag_events` (gauge) | `leader` | `ReplicaLagHigh` |
| `forge_leader_repositories` (gauge) | | replication row |
| `forge_healthy`, `forge_bgp_announced` (gauge) | | availability group |
| `forge_backup_age_seconds` (gauge) | | `BackupStale` |
| `forge_hook_decisions_total` (counter) | `hook`, `decision` | `HookDenialsSpike` |

The storage gauges (`forge_disk_free_bytes`, `forge_repositories`,
`forge_repository_bytes`, `forge_users`) are refreshed once a minute by the
daemon's stats loop; `forge_backup_age_seconds` is the age of
`<data_dir>/backup.stamp`, which `forge-backup` writes after a successful
run (no stamp: 0, which never alerts). `forge_hook_decisions_total` has no
series until the hooks record decisions (the `decision` values are not
final; the rule treats anything other than `allow|accept|ok` as a denial).

### bird_exporter

`bird_exporter` reads BIRD's control socket and exposes, per protocol,
`bird_protocol_up{name,proto,ip_version}`,
`bird_protocol_prefix_import_count`, `bird_protocol_prefix_export_count` and
route-change counters. The generated `bird.conf` names the sessions `vultr4`
and `vultr6` (`proto="BGP"`); `static4/6`, `kernel4/6` and `device` are
exported too and are cheap to keep.

Why this exporter: it is the one BIRD exporter packaged in Debian
(`prometheus-bird-exporter`, depends on `bird2 | bird3`), it needs no
configuration beyond flags, and it speaks BIRD 2's socket protocol directly;
the alternative of scraping `birdc` output with a textfile collector would
cost a cron job and a parser for the same numbers. Installed by cloud-init
on new POPs (`packages:` plus the unit as
`/etc/systemd/system/prometheus-bird-exporter.service`, which shadows the
packaged unit of the same name) and by `scripts/deploy sysupdate` on
existing ones; `9324` is in the firewall's `PRIVATE_TCP` define, which
`scripts/deploy <pop>` refreshes. The unit runs as a `DynamicUser` with
`SupplementaryGroups=bird` because Debian creates `/run/bird/bird.ctl` as
`bird:bird 0660`, and it `Requires=bird.service`, so a node without BIRD
simply never starts it.

### Where Prometheus runs

**Recommendation: a dedicated small VM (`mon1`), not a POP.** Reasons:

- A POP is a 1 GB machine already carrying forge (`MemoryMax=600M`), BIRD,
  sshd and two exporters. Prometheus plus Grafana want another 300-500 MB
  and steady disk writes; on a POP that is exactly the memory pressure
  `NodeHighMemory` exists to catch.
- Monitoring should not share fate with what it monitors. If ewr1 hosted it,
  an ewr1 outage would take the alerting with it, and the blackbox probes
  would only ever see ewr1's catchment.
- The monitoring host needs the mesh (scrapes) but not BGP, no anycast
  addresses and no public service, so its firewall is simpler than a POP's.

Cost in `docs/costs.md` terms: one `vc2-1c-1gb` at **$5.00/month** (or
`vhp-1c-1gb-intel` at $6.00 for NVMe), 25 GB of disk, of which Prometheus
needs about 2 GB per 90 days at this cardinality (roughly 3,000 series at
30 s). That raises the recommended-production total from ~$23-28 to
~$28-33. Alertmanager notifications are free (email, or a webhook to
whatever is already paid for).

### The monitoring host as code

`mon1` is the `roles: [monitor]` entry of the address plan (index 250,
mesh address `fda5:bc65:9bb1:1::fa`, Vultr `ord` so its anycast probes
cross real transit rather than a local hop). `scripts/netgen` treats a
monitor as a mesh member only: it gets a `wg0.conf` and every production
POP gets it as a peer on its mesh `/128`, but it has no BIRD config, no
anycast or `/48` unicast address, no `bgp-announce`, and
`mon1.nodes.<zone>` points at its provider addresses. The monitor's own
peers are the POPs' mesh `/128`s only. A POP's `/48` unicast address is
not probed: from outside it lands in the nearest catchment and cannot be
backhauled over the mesh (WireGuard drops packets whose source is not in
the sending peer's allowed set, and the POP forward chain drops), so those
addresses are on-net only. Per-node reachability from the internet is the
provider-address probes; the mesh is exercised by every scrape.

| Piece | What it does |
| --- | --- |
| `modules/vultr-monitor` | the instance (`vc2-1c-1gb`) and a Vultr firewall group opening only admin SSH, WireGuard and ICMP |
| `modules/monitor-node` | renders cloud-init, the monitor firewall and `prometheus.yml` (POP list in, YAML out) |
| `environments/dev/monitor.tf` | `monitors` map -> both modules; builds the POP list from the created instances; DNS; `monitor_configs` output |
| `infra/cloud-init/monitor.yaml.tftpl` | first boot from Debian only: `prometheus`, `promtool`, `prometheus-blackbox-exporter`, WireGuard, nftables; sshd on 2200 with `AllowTcpForwarding local` limited to the two loopback UIs; no forge, no BIRD, no secret bundle |
| `scripts/deploy monitor <mon>` | as root over the bootstrap key: Grafana from `apt.grafana.com` after checking the signing key against the pinned fingerprint, then `prometheus.yml` (from `tofu output monitor_configs`), rules, `blackbox.yml`, Grafana provisioning + dashboard + drop-in, `nftables.conf`, and `wg0.conf` with the key from the bundle; `promtool check config` and `nft -c` before anything is (re)loaded |

Reaching the UIs (nothing is public; Prometheus binds `[::]:9090` for the
mesh and the firewall admits it from loopback and wg0 only; Grafana binds
`127.0.0.1:3000`):

```sh
ssh -N -L 3000:127.0.0.1:3000 -L 9090:127.0.0.1:9090 -p 2200 deploy@mon1.nodes.as215520.net
# http://127.0.0.1:3000  Grafana (admin / admin at first login, then change it)
# http://127.0.0.1:9090  Prometheus (targets, /alerts, PromQL)
```

Secrets posture: the host holds exactly one secret, its WireGuard private
key, in `/etc/wireguard/wg0.conf` (0600 root) on the root filesystem. This
is deliberately simpler than a POP's tmpfs bundle (SR-02): there is no TLS
identity, host key, cluster secret or user data here, and a compromise of
mon1 yields mesh access to the POPs' *metrics ports only* (nftables on the
POPs admits `PRIVATE_TCP` from wg0, nothing else). Rotate it like any POP
key (`docs/runbooks/rotate-secrets.md`: new key in the bundle, new public
key in `infra/network/overrides.yaml`, `netgen`, `deploy monitor mon1`,
`deploy <pop>` on each POP).

Alerting (**TODO**): no Alertmanager is deployed and no destination has been
chosen. Every rule is evaluated and firing alerts are visible at
`/alerts` through the tunnel. To add one: install `prometheus-alertmanager`
on mon1 (Debian), set `alertmanager_targets = ["127.0.0.1:9093"]` in the
`monitors` map, `tofu apply`, `scripts/deploy monitor mon1`; the template
emits the `alerting:` block only when the list is non-empty
(tests/infra covers both shapes).

The dev compose stack (`infra/monitoring/dev/`) is unchanged: it points
Grafana at `host.docker.internal` and scrapes `scripts/dev-cluster` nodes;
the production provisioning files under `infra/monitoring/grafana/` are the
ones `deploy monitor` ships.
