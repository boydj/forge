output "cloud_init" {
  description = "Rendered #cloud-config user data for the provider module."
  value       = local.cloud_init
}

output "nftables_vars" {
  description = "Variables the nftables template was rendered with (for scripts/deploy or tests)."
  value       = local.nftables_vars
}

output "nftables_conf" {
  description = "Rendered /etc/nftables.conf."
  value       = local.nftables_conf
}

output "forge_toml" {
  description = "Rendered /etc/forge/forge.toml."
  value       = local.forge_toml
}

output "addresses" {
  description = "Addresses this node carries on dummy0 and wg0."
  value = {
    anycast_v4 = var.anycast_v4
    anycast_v6 = var.anycast_v6
    unicast_v6 = local.unicast_v6
    wireguard  = var.wg_address
  }
}

output "admin_ssh_port" {
  description = "Port scripts/deploy must use for OpenSSH."
  value       = var.admin_ssh_port
}
