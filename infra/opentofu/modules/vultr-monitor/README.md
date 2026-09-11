# vultr-monitor

One Vultr instance for the forge monitoring host (`mon1`): Prometheus,
blackbox_exporter and Grafana as designed in `docs/monitoring.md`. It is the
monitoring counterpart of `modules/vultr-pop`, minus everything a POP needs
and a monitor must not have: no BGP, no BYOIP/anycast addresses, no public
service ports.

| Aspect | vultr-pop | vultr-monitor |
| --- | --- | --- |
| Vultr firewall inbound | 22, 1965, 2200, 51820/udp, ICMP | 2200, 51820/udp, ICMP |
| user_data | `modules/forge-node` cloud-init | `modules/monitor-node` cloud-init |
| Region choice | near users | **away from every POP** (`ord`), so the anycast probes traverse real transit |
| Later changes | `scripts/deploy <pop>` | `scripts/deploy monitor <name>` |

`user_data` is ignored after creation (`lifecycle.ignore_changes`): the
Prometheus TSDB lives on this disk and a template edit must never recreate
the box. Wire it from `environments/dev/monitor.tf`; the runbook is
`docs/runbooks/deploy-monitor.md`.
