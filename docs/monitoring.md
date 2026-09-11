# Monitoring

Prometheus scrapes every POP over the WireGuard mesh, evaluates the alert
rules in `infra/monitoring/prometheus/rules/forge.yml`, and Grafana renders
`infra/monitoring/grafana/forge-overview.json`. Everything under
`infra/monitoring/` is the source; nothing there is generated yet.

| File | Purpose |
| --- | --- |
| `infra/monitoring/prometheus/prometheus.yml.tftpl` | Prometheus configuration, OpenTofu `templatefile()` input |
| `infra/monitoring/prometheus/rules/forge.yml` | alert rules (29) |
| `infra/monitoring/prometheus/rules/forge_test.yml` | `promtool test rules` unit tests |
| `infra/monitoring/blackbox.yml` | blackbox_exporter modules (Gemini and SSH over v4 and v6) |
| `infra/monitoring/bird-exporter.service` | systemd unit for `bird_exporter` on each POP |
| `infra/monitoring/grafana/forge-overview.json` | the dashboard |
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
| `bird` | `[<wg_address>]:9324` | 9324 | `prometheus-bird-exporter` (Debian trixie 1.4.2+ds-2, upstream `github.com/czerwonk/bird_exporter`) | unit below; **9324 is not in `PRIVATE_TCP` yet** |
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

Two of these are registered but not yet populated: `forge_backup_age_seconds`
reads 0 (a plain gauge exports 0 from registration, so `BackupStale` cannot
fire until the backup job sets it) and `forge_hook_decisions_total` has no
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
cost a cron job and a parser for the same numbers. Install: add
`prometheus-bird-exporter` to the cloud-init `packages:` list, ship
`infra/monitoring/bird-exporter.service` as
`/etc/systemd/system/prometheus-bird-exporter.service` (it shadows the
packaged unit of the same name), and add `9324` to the firewall's
`PRIVATE_TCP` define. The unit runs as a `DynamicUser` with
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

Cheaper alternative, acceptable for the first weeks: run Prometheus on the
first POP with `--storage.tsdb.retention.time=15d` and Grafana on the
operator's laptop against the dev compose stack pointed at it. Zero extra
cost, all of the drawbacks above.

Whichever host runs it needs: a mesh address (an entry in
`infra/network/address-plan.yaml` `pops:` with roles `[monitor]`, index
e.g. 250; `netgen` then emits its `wg0.conf` and the other POPs' `AllowedIPs`),
`prometheus`, `prometheus-blackbox-exporter` and `grafana` (Grafana is not in
Debian; use the upstream apt repository or a container), and the rendered
`prometheus.yml`.

Rendering the template by hand (OpenTofu ships `templatefile`):

```
cat > render.tf <<'EOF'
output "cfg" { value = templatefile("infra/monitoring/prometheus/prometheus.yml.tftpl", {
  monitor_pop = "mon1", monitor_node = "mon1", service_hostname = "git.as215520.net",
  anycast_v4 = "44.32.58.1", anycast_v6 = "2a0f:85c1:368:1::1",
  blackbox_address = "127.0.0.1:9115", alertmanager_targets = [],
  pops = [
    { name = "ewr1", wg_address = "fda5:bc65:9bb1:1::1", unicast_v6 = "2a0f:85c1:368:101::1", provider_ipv4 = "<tofu output>", provider_ipv6 = "<tofu output>" },
    { name = "ams1", wg_address = "fda5:bc65:9bb1:1::2", unicast_v6 = "2a0f:85c1:368:102::1", provider_ipv4 = "<tofu output>", provider_ipv6 = "<tofu output>" },
  ] }) }
