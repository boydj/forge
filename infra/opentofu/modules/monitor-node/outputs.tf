output "cloud_init" {
  description = "Rendered #cloud-config user data for modules/vultr-monitor."
  value       = local.cloud_init
}

output "nftables_conf" {
  description = "Rendered /etc/nftables.conf (scripts/deploy monitor refreshes it)."
  value       = local.nftables_conf
}

output "prometheus_yml" {
  description = "Rendered /etc/prometheus/prometheus.yml for the given POP list (scripts/deploy monitor pushes it)."
  value       = local.prometheus_yml
}

output "wg_ip" {
  description = "Bare WireGuard address of the monitor."
  value       = local.wg_ip
}

output "admin_ssh_port" {
  description = "Port scripts/deploy must use for OpenSSH."
  value       = var.admin_ssh_port
}
