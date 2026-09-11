# Provider-agnostic rendering for the forge monitoring host: its cloud-init,
# its nftables ruleset and the Prometheus configuration for the given POP
# list. Nothing here touches a provider; modules/vultr-monitor consumes
# `cloud_init` as user_data and `scripts/deploy monitor` reads the other two
# through the environment's outputs (like forge_toml / nftables_conf for a
# POP). See docs/monitoring.md.

locals {
  repo_root = "${path.module}/../../../.."

  wg_ip = split("/", var.wg_address)[0]

  nftables_vars = {
    admin_ssh_port = var.admin_ssh_port
    wg_port        = var.wg_port
    wg_iface       = "wg0"
    mesh_tcp_ports = join(", ", [for p in var.mesh_tcp_ports : tostring(p)])
  }
  nftables_conf = templatefile("${local.repo_root}/infra/firewall/nftables-monitor.conf.tftpl", local.nftables_vars)

  # infra/monitoring/prometheus/prometheus.yml.tftpl documents every variable.
  prometheus_vars = {
    monitor_pop          = var.name
    monitor_node         = var.name
    service_hostname     = var.service_hostname
    anycast_v4           = var.anycast_v4
    anycast_v6           = var.anycast_v6
    blackbox_address     = var.blackbox_address
    alertmanager_targets = var.alertmanager_targets
    pops                 = [for p in var.pops : p if p.provider_ipv4 != null && p.provider_ipv6 != null]
  }
  prometheus_yml = templatefile("${local.repo_root}/infra/monitoring/prometheus/prometheus.yml.tftpl", local.prometheus_vars)

  cloud_init = templatefile("${local.repo_root}/infra/cloud-init/monitor.yaml.tftpl", {
    node             = var.name
    hostname_fqdn    = var.hostname_fqdn
    service_hostname = var.service_hostname
    pop_index        = var.pop_index
    admin_ssh_port   = var.admin_ssh_port
    operator_ssh_authorized_keys = (
      length(var.operator_ssh_public_keys) == 0
      ? "      []"
      : join("\n", [for k in var.operator_ssh_public_keys : "      - ${k}"])
    )
    nftables_conf = local.nftables_conf
  })
}
