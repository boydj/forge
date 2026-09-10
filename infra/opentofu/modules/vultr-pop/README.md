# vultr-pop

One Vultr Cloud Compute instance acting as a forge point of presence, plus an
optional Vultr firewall group. Provider `vultr/vultr ~> 2.32` (2.32.0 is the
latest as of 2026-09-10; it has **no** BGP or BYOIP resources, so the BGP
request, LOA and prefix onboarding stay console/ticket work, see
`docs/research/vultr.md` sections 1-2).

The module does not know anything about forge. It takes a rendered cloud-init
document (`user_data`) from `modules/forge-node`, which is where the node
bootstrapping lives, so that every provider module boots identical nodes.

```hcl
module "node" {
  source        = "../../modules/forge-node"
  name          = "ewr1"
  hostname_fqdn = "ewr1.nodes.as215520.net"
  # ... address plan values ...
}

module "pop" {
  source = "../../modules/vultr-pop"

  name        = "ewr1"
  region      = "ewr"
  plan        = "vc2-1c-1gb"
  ssh_key_ids = [vultr_ssh_key.bootstrap.id]
  user_data   = module.node.cloud_init
  tags        = ["env:dev"]
}
```

## Inputs

| Name | Type | Default | Notes |
|---|---|---|---|
| `name` | string | required | POP name (`ewr1`); DNS label. |
| `region` | string | required | Vultr region code. `infra/providers/vultr/facts.yaml` lists all 33 with transit-filtering class; prefer `dynamic` ones first. |
| `plan` | string | `vc2-1c-1gb` | $5/mo, 1 vCPU / 1 GB / 25 GB / 1 TB. `vhp-1c-1gb-intel` ($6, NVMe, 2 TB) for prod. |
| `os_id` | number | `2625` | Debian 13. From `api.vultr.com/v2/os` (2026-09-10). Debian 12 = 2136. |
| `ssh_key_ids` | list(string) | `[]` | Bootstrap key IDs (`vultr_ssh_key`); land in root's `authorized_keys`. |
| `enable_ipv6` | bool | `true` | Needed for the IPv6 BGP session and the WireGuard mesh. |
| `create_firewall` | bool | `true` | Create and attach the forge firewall group (rules below). |
| `firewall_group_id` | string | `null` | Attach an existing group instead (when `create_firewall = false`). |
| `extra_firewall_rules` | map(object) | `{}` | Extra inbound rules merged into the created group. |
| `user_data` | string | `""` | cloud-init text; ignored after first boot (`lifecycle.ignore_changes`). |
| `tags` | list(string) | `[]` | Added to `["forge", "pop:<name>"]`. |
| `backups` | string | `"disabled"` | `"enabled"` adds +20% to the plan price and a daily 03:00 schedule. |
| `hostname` | string | `= name` | Changing it forces a reinstall. |
| `label` | string | `= name` | Console label. |
| `vpc_ids` | list(string) | `[]` | Region-bound IPv4-only VPC; unused by design. |

## Outputs

`id`, `ipv4` (main IP; the only valid IPv4 BGP source), `ipv6` (main v6; the
only valid IPv6 BGP source), `ipv6_network`, `ipv6_prefix`, `gateway_v4`,
`internal_ip` (VPC only), `hostname`, `region`, `firewall_group_id`, `status`.

## Firewall group

Created when `create_firewall = true`, inbound only, v4 and v6:

| Proto | Port | Source | Why |
|---|---|---|---|
| tcp | 22 | any | forge Git-over-SSH (the forge binary listens here; OpenSSH is moved to 2200 by cloud-init) |
| tcp | 1965 | any | Gemini / Titan |
| tcp | 2200 | any | OpenSSH for operators (key-only; nftables rate-limits it) |
| udp | 51820 | any | WireGuard mesh. Peers are not known at instance creation, so the source is open; WireGuard itself authenticates. |
| icmp | - | any | v4 ping/PMTUD; v6 ND/RA/PMTUD |

**BGP (TCP/179) is not in the list on purpose.** The session is initiated by
BIRD *from* the instance to the hypervisor-side peer (`169.254.169.254`,
`2001:19f0:ffff::1`, multihop 2). Return traffic is part of that outbound
flow. Vultr documents its firewall as inbound-only and does not document
whether it interferes with the link-local metadata/BGP peer; community
reports show BGP working behind a default-deny group. **Verify on the first
POP**: if BIRD's session sits in `Connect`/`Active`, add
`tcp/179 from 169.254.169.254/32` and `2001:19f0:ffff::1/128` through
`extra_firewall_rules` (host nftables already allows both).

Also unknown: whether the Vultr firewall filters packets destined to BYOIP
(anycast) addresses routed to the instance (`facts.yaml`:
`covers_byoip_addresses: unknown`). The host ruleset in `infra/firewall/` is
therefore the authoritative policy; the Vultr group is only an outer layer.

## What is intentionally not here

* BGP enablement, prefix onboarding, LOA: console (`Products > Network > BGP`).
* Reserved IPs: BYOIP replaces them.
* Startup scripts: cloud-init `user_data` is used instead.
* DDoS protection add-on (`ddos_protection = false`): see `docs/costs.md`.
