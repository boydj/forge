# forge-node

Provider-agnostic node definition. Given a name, the node's DNS name and its
slice of `infra/network/address-plan.yaml`, it renders:

* `cloud_init`: `infra/cloud-init/node.yaml.tftpl` (Debian 13 `#cloud-config`)
  with `infra/systemd/*`, `infra/backup/forge-backup`, the rendered
  `/etc/forge/forge.toml` and `/etc/nftables.conf` embedded;
* `nftables_conf` / `nftables_vars`: `infra/firewall/nftables.conf.tftpl`;
* `forge_toml`: `infra/cloud-init/forge.toml.tftpl`.

It has no provider and makes no API calls. A provider module (`vultr-pop`,
later `<other>-pop`) takes `cloud_init` as its `user_data`, so every provider
boots the same node. Anything provider-specific (BGP peers, BIRD config,
WireGuard peers) is *not* baked in: `scripts/deploy` pushes
`infra/bird/generated/<pop>/bird.conf` and
`infra/wireguard/generated/<pop>/wg0.conf` after first boot.

```hcl
module "node" {
  source = "../../modules/forge-node"

  name             = "ewr1"
  hostname_fqdn    = module.dns.node_fqdns["ewr1"]
  service_hostname = "git.as215520.net"
  role             = "leader"
  pop_index        = 1

  # from infra/network/address-plan.yaml
  anycast_v4       = ["44.32.58.1"]
  anycast_v6       = ["2a0f:85c1:368:1::1"]
  unicast_v6_block = "2a0f:85c1:368:101::/64"
  wg_address       = "fda5:bc65:9bb1:1::1/64"

  operator_ssh_public_keys = ["ssh-ed25519 AAAA... operator"]
}
```

## Inputs

| Name | Default | Notes |
|---|---|---|
| `name` | required | POP name; hostname; `node` in forge.toml. |
| `hostname_fqdn` | required | `<pop>.nodes.<zone>`. |
| `service_hostname` | `git.as215520.net` | Public (anycast) name. |
| `title` | `forge` | |
| `role` | `replica` | `leader` / `replica`; informational (`/etc/forge/node.env`). |
| `pop_index` | `1` | Index in the address plan; informational. |
| `anycast_v4` / `anycast_v6` | `[]` | Service addresses put on `dummy0`. |
| `unicast_v6_block` | `null` | Node's /64 from the /48; `::1` goes on `dummy0`. |
| `wg_address` | `null` | Mesh address/prefix; metrics + control bind to it. `null` = single node (metrics on 127.0.0.1, cluster off). |
| `wg_port` | `51820` | |
| `admin_ssh_port` | `2200` | OpenSSH port; forge owns 22. |
| `operator_ssh_public_keys` | `[]` | `deploy` user keys. |
| `cluster_enabled` | `false` | Needs `wg_address`. |
| `control_port` | `9200` | Cluster RPC port (wg0 only). |

## What the node looks like after first boot

| Item | Value |
|---|---|
| Users | `forge` (system, `/var/lib/forge`, nologin), `deploy` (sudo, keys above), root (bootstrap key, prohibit-password) |
| OpenSSH | port 2200, key-only, `AllowUsers root deploy` |
| Packages | git, bird2, wireguard-tools, nftables, age, zstd, curl, ca-certificates, unattended-upgrades, prometheus-node-exporter (:9101) |
| Addresses | `dummy0` via systemd-networkd `.netdev`/`.network` (only dummy0 is managed; provider NIC untouched) |
| Firewall | nftables, input drop; 22/1965 public (per-source limits), 2200 rate-limited, 51820 open, 9100/9101/9200 from wg0 only, BGP peers |
| Units | `forge.service` (enabled, waits for the binary), `forge-maintenance.timer`, `forge-backup.timer`, `bird` (placeholder config), `wg-quick@wg0` (enabled, waits for config) |
| Secrets | `/etc/forge/age.key` generated on the node; `/etc/forge/age.pub` is the recipient `scripts/deploy init-node` collects |

The cloud-init file is kept under 250 lines and validated as YAML by
`tests/infra/test_cloud_init.py` (template placeholders substituted with
dummy values).
