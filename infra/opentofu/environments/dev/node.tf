# Node definitions (provider-agnostic), one per POP. Always evaluated: they
# only render templates, so `tofu plan` shows the cloud-init even with
# create_node = false.
locals {
  # The first monitor's Prometheus over the mesh, for the /status/alerts feed
  # (monitor-node binds 9090 on wg0 and admits it from the mesh).
  prometheus_url = length(var.monitors) > 0 ? "http://[${split("/", values(var.monitors)[0].wg_address)[0]}]:9090" : ""

  # Cluster peers of each POP: every other POP's control address over WireGuard.
  cluster_peers = {
    for name, p in var.pops : name => {
      for other, o in var.pops : other => "[${split("/", o.wg_address)[0]}]:${var.control_port}" if other != name
    }
  }
}

module "node" {
  for_each = var.pops
  source   = "../../modules/forge-node"

  name             = each.key
  hostname_fqdn    = "${each.key}.nodes.${var.zone_name}"
  service_hostname = "git.${var.zone_name}"
  role             = each.value.role
  pop_index        = each.value.index

  anycast_v4       = var.anycast_v4
  anycast_v6       = var.anycast_v6
  unicast_v6_block = each.value.unicast_v6_block
  wg_address       = each.value.wg_address
  cluster_enabled  = each.value.cluster_enabled
  cluster_peers    = local.cluster_peers[each.key]
  metadata_leader  = var.metadata_leader
  control_port     = var.control_port
  bgp_announce     = each.value.bgp_announce
  docs_repo        = var.docs_repo
  prometheus_url   = local.prometheus_url
  offsite_bucket   = var.offsite_bucket

  operator_ssh_public_keys = coalesce(
    var.operator_ssh_public_keys,
    var.bootstrap_ssh_public_key == null ? [] : [var.bootstrap_ssh_public_key],
  )
}

output "cloud_init" {
  description = "Rendered user data per POP (inspect with `tofu output -json cloud_init`)."
  value       = { for name, n in module.node : name => n.cloud_init }
  sensitive   = true # contains the operator public keys and full config
}

# scripts/deploy reads these with `tofu output -json` to refresh a node's
# config after first boot (they contain no secrets).
output "forge_toml" {
  description = "Rendered /etc/forge/forge.toml per POP."
  value       = { for name, n in module.node : name => n.forge_toml }
}

output "forge_backup_env" {
  description = "Rendered /etc/default/forge-backup per POP (empty: no off-site copy)."
  value       = { for name, n in module.node : name => n.forge_backup_env }
}

output "nftables_conf" {
  description = "Rendered /etc/nftables.conf per POP."
  value       = { for name, n in module.node : name => n.nftables_conf }
}
