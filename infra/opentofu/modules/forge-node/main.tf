# Provider-agnostic node definition: renders cloud-init user data (and the
# files it embeds) so that every provider module boots an identical forge
# node. Nothing here talks to an API.

locals {
  repo_root = "${path.module}/../../../.."

  # Addresses that live on dummy0 (systemd-networkd .network Address= lines).
  unicast_v6 = var.unicast_v6_block == null ? null : "${cidrhost(var.unicast_v6_block, 1)}/${split("/", var.unicast_v6_block)[1]}"
  dummy_addresses = concat(
    [for ip in var.anycast_v4 : "Address=${ip}/32"],
    [for ip in var.anycast_v6 : "Address=${ip}/128"],
    local.unicast_v6 == null ? [] : ["Address=${local.unicast_v6}"],
  )

  wg_ip           = var.wg_address == null ? null : split("/", var.wg_address)[0]
  metrics_listen  = local.wg_ip == null ? "127.0.0.1:9100" : "[${local.wg_ip}]:9100"
  control_listen  = local.wg_ip == null ? "" : "[${local.wg_ip}]:${var.control_port}"
  cluster_enabled = var.cluster_enabled && local.wg_ip != null

  nftables_vars = {
    anycast_v4     = length(var.anycast_v4) > 0 ? join(", ", var.anycast_v4) : "192.0.2.0/32"
    anycast_v6     = length(var.anycast_v6) > 0 ? join(", ", var.anycast_v6) : "100::/128"
    admin_ssh_port = var.admin_ssh_port
    wg_port        = var.wg_port
    wg_iface       = "wg0"
    # forge metrics, node-exporter, bird-exporter, cluster control RPC.
    private_tcp_ports = "9100, 9101, 9324, ${var.control_port}"
  }
  nftables_conf = templatefile("${local.repo_root}/infra/firewall/nftables.conf.tftpl", local.nftables_vars)

  forge_toml = templatefile("${local.repo_root}/infra/cloud-init/forge.toml.tftpl", {
    node               = var.name
    service_hostname   = var.service_hostname
    title              = var.title
    gemini_listen      = jsonencode([":1965"]) # TOML string array == JSON array
    ssh_listen         = jsonencode([":22"])
    metrics_listen     = local.metrics_listen
    cluster_enabled    = local.cluster_enabled
    control_listen     = local.control_listen
    cluster_peers_toml = join("\n", [for n, a in var.cluster_peers : "${n} = \"${a}\""])
    metadata_leader    = var.metadata_leader
    announcer          = var.bgp_announce ? "/usr/local/bin/forge-bgp-request" : ""
    docs_repo          = var.docs_repo
    prometheus_url     = var.prometheus_url
  })

  cloud_init = templatefile("${local.repo_root}/infra/cloud-init/node.yaml.tftpl", {
    node             = var.name
    hostname_fqdn    = var.hostname_fqdn
    service_hostname = var.service_hostname
    role             = var.role
    pop_index        = var.pop_index
    admin_ssh_port   = var.admin_ssh_port
    operator_ssh_authorized_keys = (
      length(var.operator_ssh_public_keys) == 0
      ? "      []"
      : join("\n", [for k in var.operator_ssh_public_keys : "      - ${k}"])
    )
    anycast_network_addresses = join("\n", local.dummy_addresses)
    forge_toml                = local.forge_toml
    nftables_conf             = local.nftables_conf
    forge_service             = file("${local.repo_root}/infra/systemd/forge.service")
    forge_maintenance_service = file("${local.repo_root}/infra/systemd/forge-maintenance.service")
    forge_maintenance_timer   = file("${local.repo_root}/infra/systemd/forge-maintenance.timer")
    forge_backup_service      = file("${local.repo_root}/infra/systemd/forge-backup.service")
    forge_backup_timer        = file("${local.repo_root}/infra/systemd/forge-backup.timer")
    forge_backup_script       = file("${local.repo_root}/infra/backup/forge-backup")
    forge_bgp_service         = file("${local.repo_root}/infra/systemd/forge-bgp.service")
    forge_bgp_path            = file("${local.repo_root}/infra/systemd/forge-bgp.path")
    forge_bgp_request         = file("${local.repo_root}/infra/systemd/forge-bgp-request")
    forge_bgp_exec            = file("${local.repo_root}/infra/systemd/forge-bgp-exec")
    bgp_announce_script       = file("${local.repo_root}/scripts/bgp-announce")
    bird_exporter_service     = file("${local.repo_root}/infra/monitoring/bird-exporter.service")
  })
}
