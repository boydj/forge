variable "name" {
  description = "Node/POP name, e.g. \"ewr1\". Becomes the hostname and forge's `node` key."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.name))
    error_message = "name must be a lower-case DNS label."
  }
}

variable "hostname_fqdn" {
  description = "Node FQDN, e.g. \"ewr1.nodes.as215520.net\" (the cloudflare-dns module's node_fqdns output)."
  type        = string
}

variable "service_hostname" {
  description = "Public service name forge builds URLs with, e.g. \"git.as215520.net\" (anycast)."
  type        = string
  default     = "git.as215520.net"
}

variable "title" {
  description = "Forge title shown on pages."
  type        = string
  default     = "forge"
}

variable "role" {
  description = "leader or replica (see docs/decisions/0011). Written to /etc/forge/node.env; forge itself learns leadership per repository."
  type        = string
  default     = "replica"

  validation {
    condition     = contains(["leader", "replica"], var.role)
    error_message = "role must be \"leader\" or \"replica\"."
  }
}

variable "pop_index" {
  description = "Small integer identifying the POP in infra/network/address-plan.yaml (drives the unicast /64 and WireGuard address). Informational here."
  type        = number
  default     = 1
}

variable "anycast_v4" {
  description = "Anycast IPv4 service addresses (from address-plan.yaml), added to dummy0 as /32 and allowed in nftables."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for ip in var.anycast_v4 : can(cidrhost("${ip}/32", 0))])
    error_message = "anycast_v4 entries must be plain IPv4 addresses."
  }
}

variable "anycast_v6" {
  description = "Anycast IPv6 service addresses (from address-plan.yaml), added to dummy0 as /128."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for ip in var.anycast_v6 : can(cidrhost("${ip}/128", 0))])
    error_message = "anycast_v6 entries must be plain IPv6 addresses."
  }
}

variable "unicast_v6_block" {
  description = "This node's own /64 out of the operator's /48 (address-plan.yaml), e.g. \"2a0f:85c1:368:1::/64\"; ::1 of it is added to dummy0 as a BYOIP unicast address. null = none (single-node installs)."
  type        = string
  default     = null

  validation {
    condition     = var.unicast_v6_block == null || can(cidrhost(var.unicast_v6_block, 1))
    error_message = "unicast_v6_block must be an IPv6 CIDR."
  }
}

variable "wg_address" {
  description = "WireGuard mesh address with prefix length, e.g. \"fd42:2155:2000::1/64\" (address-plan.yaml). The metrics and cluster control listeners bind to it. null = single node: metrics on loopback, cluster disabled."
  type        = string
  default     = null

  validation {
    condition     = var.wg_address == null || can(cidrhost(var.wg_address, 0))
    error_message = "wg_address must be an address with prefix length, e.g. fd42::1/64."
  }
}

variable "wg_port" {
  description = "WireGuard UDP port."
  type        = number
  default     = 51820
}

variable "admin_ssh_port" {
  description = "Port OpenSSH is moved to so forge can own 22."
  type        = number
  default     = 2200
}

variable "operator_ssh_public_keys" {
  description = "Public keys allowed to log in as `deploy` (and used by scripts/deploy). OpenSSH authorized_keys lines."
  type        = list(string)
  default     = []
}

variable "cluster_enabled" {
  description = "Enable replication/leader forwarding. Requires wg_address."
  type        = bool
  default     = false
}

variable "control_port" {
  description = "forge cluster control RPC port on the WireGuard address."
  type        = number
  default     = 9200
}

variable "bgp_announce" {
  description = "Let the health controller announce the anycast prefixes through scripts/bgp-announce once the node passes its checks. false = BGP sessions stay up but export nothing (safe default until the operator goes live, ADR 0013)."
  type        = bool
  default     = false
}

variable "cluster_peers" {
  description = "Other nodes of the cluster: name => control address ([wg address]:port). Rendered into [cluster.peers]."
  type        = map(string)
  default     = {}
}

variable "metadata_leader" {
  description = "Node that owns users, certificates and SSH keys (cluster.metadata_leader). Empty = alphabetically first node."
  type        = string
  default     = ""
}

variable "docs_repo" {
  description = "Public repository (owner/name) on this forge whose docs/ directory is published at /docs/ (forge.toml docs.repo). Empty = no documentation site."
  type        = string
  default     = ""

  validation {
    condition     = var.docs_repo == "" || can(regex("^[a-z0-9._-]+/[a-z0-9._-]+$", var.docs_repo))
    error_message = "docs_repo must be \"owner/name\" or empty."
  }
}

variable "prometheus_url" {
  description = "Base URL of the monitoring host's Prometheus over the control network (forge.toml status.prometheus_url); its firing alerts are served as the /status/alerts gemfeed. Empty = no alerts feed."
  type        = string
  default     = ""

  validation {
    condition     = var.prometheus_url == "" || can(regex("^https?://", var.prometheus_url))
    error_message = "prometheus_url must be empty or start with http:// or https://."
  }
}

variable "offsite_bucket" {
  description = "Backblaze B2 bucket for off-site backups (infra/opentofu/environments/b2). Renders /etc/default/forge-backup so forge-backup copies each archive there with forge-offsite; empty = no off-site copy. Set it only once b2_backup_key_id/b2_backup_key are in the bundle."
  type        = string
  default     = ""
}

variable "mirrors" {
  description = "Repositories mirrored to an external remote after every push and hourly: \"owner/name\" => ssh or https URL (forge.toml [[mirrors]]). The ssh deploy key is mirror_deploy_key in the bundle; without it mirroring stays disabled."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for repo, url in var.mirrors : can(regex("^[a-z][a-z0-9-]*/[a-z0-9][a-z0-9._-]*$", repo)) && can(regex("^(git@[^:]+:.+|ssh://.+|https://.+)$", url))])
    error_message = "mirrors keys must be owner/name and values git@host:path, ssh:// or https:// URLs."
  }
}
