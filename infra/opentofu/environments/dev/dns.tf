module "dns" {
  source = "../../modules/cloudflare-dns"

  zone_name        = var.zone_name
  zone_id          = var.zone_id
  service_hostname = "git"

  anycast_v4 = var.anycast_v4
  anycast_v6 = var.anycast_v6
  nodes      = var.nodes
  sshfp      = var.sshfp

  ttl           = 300
  manage_dnssec = var.manage_dnssec
  comment       = "managed by opentofu (forge/environments/dev)"
}

output "service_fqdn" {
  value = module.dns.service_fqdn
}

output "service_urls" {
  value = module.dns.service_urls
}

output "node_fqdns" {
  value = module.dns.node_fqdns
}

output "dnssec_ds" {
  description = "Publish this DS at the registrar after enabling DNSSEC."
  value       = module.dns.dnssec_ds
}
