output "id" {
  description = "Vultr instance ID."
  value       = vultr_instance.this.id
}

output "ipv4" {
  description = "Main public IPv4 (the monitor's only IPv4; <name>.nodes.<zone> A record)."
  value       = vultr_instance.this.main_ip
}

output "ipv6" {
  description = "Main public IPv6 (WireGuard endpoint; <name>.nodes.<zone> AAAA record)."
  value       = vultr_instance.this.v6_main_ip
}

output "ipv6_network" {
  description = "IPv6 network assigned to the instance (a /64)."
  value       = vultr_instance.this.v6_network
}

output "ipv6_prefix" {
  description = "Prefix length of ipv6_network (64)."
  value       = vultr_instance.this.v6_network_size
}

output "hostname" {
  description = "Instance hostname as set at creation."
  value       = local.hostname
}

output "region" {
  description = "Region the instance was created in."
  value       = vultr_instance.this.region
}

output "firewall_group_id" {
  description = "Firewall group attached to the instance (created or passed in), null if none."
  value       = local.firewall_group_id
}

output "status" {
  description = "Vultr instance status / power_status."
  value = {
    status = vultr_instance.this.status
    power  = vultr_instance.this.power_status
  }
}
