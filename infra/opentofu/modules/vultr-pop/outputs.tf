output "id" {
  description = "Vultr instance ID."
  value       = vultr_instance.this.id
}

output "ipv4" {
  description = "Main public IPv4 (the only address Vultr accepts as the IPv4 BGP session source)."
  value       = vultr_instance.this.main_ip
}

output "ipv6" {
  description = "Main public IPv6 (the only address Vultr accepts as the IPv6 BGP session source)."
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

output "gateway_v4" {
  description = "IPv4 gateway (IPv6 gateway is always fe80::1 on Vultr)."
  value       = vultr_instance.this.gateway_v4
}

output "internal_ip" {
  description = "VPC address when vpc_ids is set; empty otherwise."
  value       = vultr_instance.this.internal_ip
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
