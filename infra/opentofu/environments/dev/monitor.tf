# The monitoring host (docs/monitoring.md): one modules/vultr-monitor
# instance per entry of var.monitors (normally just mon1), its cloud-init and
# firewall from modules/monitor-node, and the Prometheus configuration for
# every created POP. Like the POPs it is gated by create_node so DNS-only
# applies never need VULTR_API_KEY. Runbook: docs/runbooks/deploy-monitor.md.

locals {
  # Scrape/probe targets: every created POP, with the provider addresses the
  # instances actually got. Empty when create_node = false, so the rendered
  # prometheus.yml has no POP targets rather than placeholders.
  monitor_pops = [
    for name, p in module.pop : {
      name          = name
      wg_address    = split("/", var.pops[name].wg_address)[0]
      unicast_v6    = cidrhost(var.pops[name].unicast_v6_block, 1)
      provider_ipv4 = p.ipv4
      provider_ipv6 = p.ipv6
    }
  ]

  # Provider addresses of every created monitor, merged into the DNS node map
  # (a monitor has no /48 address, so <name>.nodes.<zone> is provider-only).
  monitor_dns_nodes = {
    for name, m in module.monitor : name => { ipv4 = m.ipv4, ipv6 = m.ipv6 }
  }
}

module "monitor_node" {
  for_each = var.monitors
  source   = "../../modules/monitor-node"

  name             = each.key
  hostname_fqdn    = "${each.key}.nodes.${var.zone_name}"
  service_hostname = "git.${var.zone_name}"
  pop_index        = each.value.index
  wg_address       = each.value.wg_address
  anycast_v4       = var.anycast_v4[0]
  anycast_v6       = var.anycast_v6[0]
  pops             = local.monitor_pops

  alertmanager_targets = each.value.alertmanager_targets

  operator_ssh_public_keys = coalesce(
    var.operator_ssh_public_keys,
    var.bootstrap_ssh_public_key == null ? [] : [var.bootstrap_ssh_public_key],
  )
}

module "monitor" {
  for_each = var.create_node ? var.monitors : {}
  source   = "../../modules/vultr-monitor"

  name        = each.key
  region      = each.value.region
  plan        = each.value.plan
  os_id       = var.vultr_os_id
  ssh_key_ids = [vultr_ssh_key.bootstrap[0].id]
  user_data   = module.monitor_node[each.key].cloud_init
  tags        = ["env:dev"]
  backups     = "disabled"

  create_firewall = true
}

output "monitors" {
  description = "Monitor facts (empty when create_node = false)."
  value = {
    for name, m in module.monitor : name => {
      id        = m.id
      ipv4      = m.ipv4
      ipv6      = m.ipv6
      region    = m.region
      admin_ssh = "ssh -p ${module.monitor_node[name].admin_ssh_port} root@${name}.nodes.${var.zone_name}"
      grafana   = "ssh -N -L 3000:127.0.0.1:3000 -L 9090:127.0.0.1:9090 -p ${module.monitor_node[name].admin_ssh_port} deploy@${name}.nodes.${var.zone_name}"
    }
  }
}

# scripts/deploy monitor reads these with `tofu output -json` (no secrets).
output "monitor_configs" {
  description = "Rendered prometheus.yml and nftables.conf per monitor."
  value = {
    for name, n in module.monitor_node : name => {
      prometheus_yml = n.prometheus_yml
      nftables_conf  = n.nftables_conf
    }
  }
}

output "monitor_cloud_init" {
  description = "Rendered user data per monitor (inspect with `tofu output -json monitor_cloud_init`)."
  value       = { for name, n in module.monitor_node : name => n.cloud_init }
  sensitive   = true # contains the operator public keys
}
