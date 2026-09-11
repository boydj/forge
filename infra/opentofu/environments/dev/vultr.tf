# POPs on Vultr, one instance per entry of var.pops. Toggle with create_node
# so DNS-only applies never touch instances (and never need VULTR_API_KEY).

resource "vultr_ssh_key" "bootstrap" {
  count = var.create_node ? 1 : 0

  name    = "forge-bootstrap"
  ssh_key = var.bootstrap_ssh_public_key

  lifecycle {
    precondition {
      condition     = var.bootstrap_ssh_public_key != null
      error_message = "bootstrap_ssh_public_key is required when create_node = true."
    }
  }
}

module "pop" {
  for_each = var.create_node ? var.pops : {}
  source   = "../../modules/vultr-pop"

  name        = each.key
  region      = each.value.region
  plan        = each.value.plan
  os_id       = var.vultr_os_id
  ssh_key_ids = [vultr_ssh_key.bootstrap[0].id]
  user_data   = module.node[each.key].cloud_init
  tags        = ["env:dev"]
  backups     = "disabled"

  create_firewall = true
}

locals {
  # Provider-assigned unicast addresses of every created POP, merged into
  # the DNS node map so <pop>.nodes.<zone> follows the instances.
  pop_dns_nodes = {
    for name, p in module.pop : name => { ipv4 = p.ipv4, ipv6 = p.ipv6 }
  }
}

output "pops" {
  description = "POP facts (empty when create_node = false)."
  value = {
    for name, p in module.pop : name => {
      id           = p.id
      ipv4         = p.ipv4
      ipv6         = p.ipv6
      ipv6_network = "${p.ipv6_network}/${p.ipv6_prefix}"
      region       = p.region
      admin_ssh    = "ssh -p ${module.node[name].admin_ssh_port} deploy@${name}.nodes.${var.zone_name}"
    }
  }
}
