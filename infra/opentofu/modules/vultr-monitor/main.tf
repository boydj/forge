# One Vultr Cloud Compute instance acting as the forge monitoring host
# (Prometheus, blackbox_exporter, Grafana; docs/monitoring.md), with an
# optional Vultr firewall group. Compared with modules/vultr-pop this machine
# has no BGP session, no anycast/BYOIP addresses and no public service, so
# the belt-and-braces Vultr firewall only opens operator SSH, the WireGuard
# port and ICMP; the host nftables ruleset (infra/firewall/
# nftables-monitor.conf.tftpl) is the real policy.

locals {
  hostname = coalesce(var.hostname, var.name)
  label    = coalesce(var.label, var.name)
  tags     = distinct(concat(["forge", "monitor:${var.name}"], var.tags))

  base_rules = {
    ssh_admin_v4 = { protocol = "tcp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = tostring(var.admin_ssh_port), notes = "openssh admin" }
    ssh_admin_v6 = { protocol = "tcp", ip_type = "v6", subnet = "::", subnet_size = 0, port = tostring(var.admin_ssh_port), notes = "openssh admin" }
    wireguard_v4 = { protocol = "udp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = tostring(var.wg_port), notes = "wireguard mesh (peers unknown at create time)" }
    wireguard_v6 = { protocol = "udp", ip_type = "v6", subnet = "::", subnet_size = 0, port = tostring(var.wg_port), notes = "wireguard mesh (peers unknown at create time)" }
    icmp_v4      = { protocol = "icmp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = null, notes = "icmp (ping, pmtud)" }
    icmp_v6      = { protocol = "icmp", ip_type = "v6", subnet = "::", subnet_size = 0, port = null, notes = "icmpv6 (nd, pmtud)" }
  }
  firewall_rules    = var.create_firewall ? merge(local.base_rules, var.extra_firewall_rules) : {}
  firewall_group_id = var.create_firewall ? vultr_firewall_group.this[0].id : var.firewall_group_id
}

resource "vultr_firewall_group" "this" {
  count = var.create_firewall ? 1 : 0

  description = "forge monitor ${var.name}"
}

resource "vultr_firewall_rule" "this" {
  for_each = local.firewall_rules

  firewall_group_id = vultr_firewall_group.this[0].id
  protocol          = each.value.protocol
  ip_type           = each.value.ip_type
  subnet            = each.value.subnet
  subnet_size       = each.value.subnet_size
  port              = each.value.port
  notes             = each.value.notes
}

resource "vultr_instance" "this" {
  region   = var.region
  plan     = var.plan
  os_id    = var.os_id
  hostname = local.hostname
  label    = local.label
  tags     = local.tags

  enable_ipv6       = true # the mesh is IPv6-only: the WireGuard endpoint is the provider IPv6
  ssh_key_ids       = var.ssh_key_ids
  firewall_group_id = local.firewall_group_id
  user_data         = var.user_data
  backups           = var.backups
  activation_email  = false
  ddos_protection   = false

  lifecycle {
    # cloud-init runs once; later template changes must not recreate the box
    # (Prometheus data lives here). Push changes with `scripts/deploy monitor`.
    ignore_changes = [user_data]
    precondition {
      condition     = var.create_firewall || var.firewall_group_id != null || length(var.extra_firewall_rules) == 0
      error_message = "extra_firewall_rules requires create_firewall = true."
    }
  }
}
