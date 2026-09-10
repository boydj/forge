output "zone_id" {
  description = "Cloudflare zone ID used for every record."
  value       = local.zone_id
}

output "service_fqdn" {
  description = "FQDN of the anycast service, e.g. git.as215520.net."
  value       = local.service_fqdn
}

output "service_urls" {
  description = "Convenience endpoints for the service hostname."
  value = {
    gemini = "gemini://${local.service_fqdn}/"
    ssh    = "git@${local.service_fqdn}"
  }
}

output "node_fqdns" {
  description = "Map of POP name => FQDN, e.g. ewr1 => ewr1.nodes.as215520.net."
  value       = { for name, _ in var.nodes : name => "${name}.${var.nodes_subdomain}.${var.zone_name}" }
}

output "service_record_ids" {
  description = "Record IDs for the service A/AAAA records keyed by address."
  value = merge(
    { for ip, r in cloudflare_dns_record.service_a : ip => r.id },
    { for ip, r in cloudflare_dns_record.service_aaaa : ip => r.id },
  )
}

output "sshfp_record_ids" {
  description = "Record IDs for SSHFP records keyed by \"<algorithm>-<fptype>\"."
  value       = { for k, r in cloudflare_dns_record.service_sshfp : k => r.id }
}

output "node_record_ids" {
  description = "Record IDs for node records keyed by \"<pop>-a\" / \"<pop>-aaaa\"."
  value       = { for k, r in cloudflare_dns_record.node : k => r.id }
}

output "extra_record_ids" {
  description = "Record IDs for extra_records keyed by the caller's key."
  value       = { for k, r in cloudflare_dns_record.extra : k => r.id }
}

output "dnssec_ds" {
  description = "DS record to publish at the registrar when manage_dnssec = true (null otherwise)."
  value       = var.manage_dnssec ? cloudflare_zone_dnssec.this[0].ds : null
}
