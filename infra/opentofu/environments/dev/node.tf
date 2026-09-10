# Node definition (provider-agnostic). Always evaluated: it only renders
# templates, so `tofu plan` shows the cloud-init even with create_node=false.
module "node" {
  source = "../../modules/forge-node"

  name             = var.pop_name
  hostname_fqdn    = "${var.pop_name}.nodes.${var.zone_name}"
  service_hostname = "git.${var.zone_name}"
  role             = "leader" # single dev node owns every repository
  pop_index        = var.pop_index

  anycast_v4       = var.anycast_v4
  anycast_v6       = var.anycast_v6
  unicast_v6_block = var.unicast_v6_block
  wg_address       = var.wg_address
  cluster_enabled  = var.cluster_enabled

  operator_ssh_public_keys = coalesce(
    var.operator_ssh_public_keys,
    var.bootstrap_ssh_public_key == null ? [] : [var.bootstrap_ssh_public_key],
  )
}

output "cloud_init" {
  description = "Rendered user data for the dev node (inspect with `tofu output -raw cloud_init`)."
  value       = module.node.cloud_init
  sensitive   = true # contains the operator public keys and full config; keep plan output short
}

# scripts/deploy reads these with `tofu output -raw` to refresh the node's
# config after first boot (they contain no secrets).
output "forge_toml" {
  description = "Rendered /etc/forge/forge.toml for the dev node."
  value       = module.node.forge_toml
}

output "nftables_conf" {
  description = "Rendered /etc/nftables.conf for the dev node."
  value       = module.node.nftables_conf
}
