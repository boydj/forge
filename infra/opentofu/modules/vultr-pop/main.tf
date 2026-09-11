# One Vultr Cloud Compute instance acting as a forge POP, with an optional
# Vultr firewall group. BGP and BYOIP are not resources in the Vultr provider
# (2.32.0): they are account-level, human-reviewed steps documented in
# docs/research/vultr.md. This module only creates the machine the session
# will run from.

locals {
  hostname = coalesce(var.hostname, var.name)
  label    = coalesce(var.label, var.name)
  tags     = distinct(concat(["forge", "pop:${var.name}"], var.tags))

  # Inbound rules for the created firewall group. Vultr's firewall is
  # inbound-only, stateless from the user's point of view, and applied per
  # instance to the provider-assigned addresses. Whether it also filters
  # traffic to BYOIP (anycast) addresses routed to the instance is not
  # documented (facts.yaml: covers_byoip_addresses: unknown), so the host
  # nftables ruleset in infra/firewall/ is the real policy; this set is a
  # belt-and-braces layer that stops the noisiest scans before the NIC.
  #
  # TCP/179 is deliberately absent: the BGP session is opened *outbound* from
  # the instance to 169.254.169.254 / 2001:19f0:ffff::1 (multihop 2) and the
  # reply packets belong to that established flow. Vultr's firewall does not
  # track state, but it also does not sit between the hypervisor's link-local
  # peer and the guest; field reports of BGP working with a default-deny
  # firewall group exist (vojk.au). VERIFY on first deploy: if the session
  # stays in Connect/Active, add tcp/179 from 169.254.169.254/32 and
  # 2001:19f0:ffff::1/128 via extra_firewall_rules.
  base_rules = {
    ssh_forge_v4 = { protocol = "tcp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = "22", notes = "forge git-over-ssh" }
    ssh_forge_v6 = { protocol = "tcp", ip_type = "v6", subnet = "::", subnet_size = 0, port = "22", notes = "forge git-over-ssh" }
    gemini_v4    = { protocol = "tcp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = "1965", notes = "gemini/titan" }
    gemini_v6    = { protocol = "tcp", ip_type = "v6", subnet = "::", subnet_size = 0, port = "1965", notes = "gemini/titan" }
    ssh_admin_v4 = { protocol = "tcp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = "2200", notes = "openssh admin" }
    ssh_admin_v6 = { protocol = "tcp", ip_type = "v6", subnet = "::", subnet_size = 0, port = "2200", notes = "openssh admin" }
    wireguard_v4 = { protocol = "udp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = "51820", notes = "wireguard mesh (peers unknown at create time)" }
    wireguard_v6 = { protocol = "udp", ip_type = "v6", subnet = "::", subnet_size = 0, port = "51820", notes = "wireguard mesh (peers unknown at create time)" }
    icmp_v4      = { protocol = "icmp", ip_type = "v4", subnet = "0.0.0.0", subnet_size = 0, port = null, notes = "icmp (ping, pmtud)" }
    icmp_v6      = { protocol = "icmp", ip_type = "v6", subnet = "::", subnet_size = 0, port = null, notes = "icmpv6 (nd, pmtud)" }
  }
  firewall_rules = var.create_firewall ? merge(local.base_rules, var.extra_firewall_rules) : {}

  firewall_group_id = var.create_firewall ? vultr_firewall_group.this[0].id : var.firewall_group_id
}

resource "vultr_firewall_group" "this" {
  count = var.create_firewall ? 1 : 0

  description = "forge pop ${var.name}"
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

  enable_ipv6       = var.enable_ipv6
  ssh_key_ids       = var.ssh_key_ids
  firewall_group_id = local.firewall_group_id
  user_data         = var.user_data
  backups           = var.backups
  vpc_ids           = var.vpc_ids

  # The API requires a schedule whenever backups are enabled.
  dynamic "backups_schedule" {
    for_each = var.backups == "enabled" ? [1] : []
    content {
      type = "daily"
      hour = 3
    }
  }

  # No e-mail per deploy; DDoS protection is a paid add-on decided per
  # environment (docs/costs.md), off by default.
  activation_email = false
  ddos_protection  = false

  lifecycle {
    # user_data only runs at first boot; a changed template must not
    # reinstall a live POP. Taint/replace deliberately when you want that.
    ignore_changes = [user_data]

    precondition {
      condition     = var.create_firewall || var.firewall_group_id != null || length(var.extra_firewall_rules) == 0
      error_message = "extra_firewall_rules requires create_firewall = true."
    }
  }
}
