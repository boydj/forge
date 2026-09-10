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
