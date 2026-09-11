# monitor-node

Renders everything the forge monitoring host needs, from the repository's
templates, with no provider involved (the counterpart of `modules/forge-node`
for a `roles: [monitor]` entry of `infra/network/address-plan.yaml`):

| Output | Template | Consumer |
| --- | --- | --- |
| `cloud_init` | `infra/cloud-init/monitor.yaml.tftpl` | `modules/vultr-monitor` `user_data` (first boot only) |
| `nftables_conf` | `infra/firewall/nftables-monitor.conf.tftpl` | cloud-init, then `scripts/deploy monitor` |
| `prometheus_yml` | `infra/monitoring/prometheus/prometheus.yml.tftpl` | `scripts/deploy monitor` (`tofu output -json monitor_configs`) |

`pops` is the scrape/probe list. `environments/dev/monitor.tf` builds it
from the `pops` map plus the created instances' addresses, so with
`create_node = false` the list is empty and the rendered `prometheus.yml`
has no POP targets; with real instances every POP gets its `forge`, `node`
and `bird` scrape targets on the WireGuard mesh and its blackbox probes on
the provider and `/48` unicast addresses. Alerts, dashboards and blackbox
modules are plain files under `infra/monitoring/`, pushed by the same deploy
command; nothing large is embedded in cloud-init (tests/infra keeps the
template under a line cap).
