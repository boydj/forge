module "dns" {
  count  = var.create_dns ? 1 : 0
  source = "../../modules/cloudflare-dns"

  zone_name        = var.zone_name
  zone_id          = var.zone_id
  service_hostname = "git"

  anycast_v4 = var.anycast_v4
  anycast_v6 = var.anycast_v6
  nodes      = merge(var.nodes, local.pop_dns_nodes, local.monitor_dns_nodes)
  sshfp      = var.sshfp

  ttl           = 300
  manage_dnssec = var.manage_dnssec
  comment       = "managed by opentofu (forge/environments/dev)"
}

output "service_fqdn" {
  value = var.create_dns ? module.dns[0].service_fqdn : null
}

output "service_urls" {
  value = var.create_dns ? module.dns[0].service_urls : null
}

output "node_fqdns" {
  value = var.create_dns ? module.dns[0].node_fqdns : null
}

output "dnssec_ds" {
  description = "Publish this DS at the registrar after enabling DNSSEC."
  value       = var.create_dns ? module.dns[0].dnssec_ds : null
}
