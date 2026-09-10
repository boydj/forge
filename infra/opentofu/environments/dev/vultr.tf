# Dev POP on Vultr. Toggle with create_node so DNS-only applies never touch
# the instance (and never need VULTR_API_KEY).

resource "vultr_ssh_key" "bootstrap" {
  count = var.create_node ? 1 : 0

  name    = "forge-bootstrap-${var.pop_name}"
  ssh_key = var.bootstrap_ssh_public_key

  lifecycle {
    precondition {
      condition     = var.bootstrap_ssh_public_key != null
      error_message = "bootstrap_ssh_public_key is required when create_node = true."
    }
  }
}

module "pop" {
  count  = var.create_node ? 1 : 0
  source = "../../modules/vultr-pop"

  name        = var.pop_name
  region      = var.vultr_region
  plan        = var.vultr_plan
  os_id       = var.vultr_os_id
  ssh_key_ids = [vultr_ssh_key.bootstrap[0].id]
  user_data   = module.node.cloud_init
  tags        = ["env:dev"]
  backups     = "disabled"

  create_firewall = true
}

locals {
  # Provider-assigned unicast addresses of the dev POP, if it exists, merged
  # into the DNS node map so <pop>.nodes.<zone> follows the instance.
  pop_dns_node = var.create_node ? {
    (var.pop_name) = {
      ipv4 = module.pop[0].ipv4
      ipv6 = module.pop[0].ipv6
    }
  } : {}
}

output "pop" {
  description = "Dev POP facts (null when create_node = false)."
  value = var.create_node ? {
    id           = module.pop[0].id
    ipv4         = module.pop[0].ipv4
    ipv6         = module.pop[0].ipv6
    ipv6_network = "${module.pop[0].ipv6_network}/${module.pop[0].ipv6_prefix}"
    region       = module.pop[0].region
    admin_ssh    = "ssh -p ${module.node.admin_ssh_port} deploy@${var.pop_name}.nodes.${var.zone_name}"
  } : null
}
