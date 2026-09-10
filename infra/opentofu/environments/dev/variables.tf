# --- DNS (modules/cloudflare-dns)

variable "zone_name" {
  description = "Cloudflare zone to publish into."
  type        = string
  default     = "as215520.net"
}

variable "zone_id" {
  description = "Optional zone ID to skip the cloudflare_zones lookup."
  type        = string
  default     = null
}

variable "anycast_v4" {
  description = "Anycast IPv4 addresses for git.<zone> (infra/network/address-plan.yaml)."
  type        = list(string)
}

variable "anycast_v6" {
  description = "Anycast IPv6 addresses for git.<zone> (infra/network/address-plan.yaml)."
  type        = list(string)
}

variable "nodes" {
  description = "POP name => unicast addresses for <pop>.nodes.<zone>. When create_node = true the dev POP's provider addresses are merged in automatically; list other/static nodes here."
  type = map(object({
    ipv4 = optional(string)
    ipv6 = optional(string)
  }))
  default = {}
}

variable "sshfp" {
  description = "SSHFP records for git.<zone> (from ssh-keygen -r)."
  type = list(object({
    algorithm   = number
    type        = number
    fingerprint = string
  }))
  default = []
}

variable "manage_dnssec" {
  description = "Enable DNSSEC on the zone via OpenTofu."
  type        = bool
  default     = false
}

variable "create_dns" {
  description = "Manage DNS records. false = node-only apply (CLOUDFLARE_API_TOKEN not needed)."
  type        = bool
  default     = true
}

# --- Node (modules/forge-node + modules/vultr-pop)

variable "create_node" {
  description = "Create the dev POP on Vultr. false = DNS-only apply (VULTR_API_KEY not needed)."
  type        = bool
  default     = false
}

variable "pop_name" {
  description = "Dev POP name."
  type        = string
  default     = "ewr1"
}

variable "pop_index" {
  description = "Index of the POP in infra/network/address-plan.yaml."
  type        = number
  default     = 1
}

variable "vultr_region" {
  description = "Vultr region for the dev POP."
  type        = string
  default     = "ewr"
}

variable "vultr_plan" {
  description = "Vultr plan for the dev POP."
  type        = string
  default     = "vc2-1c-1gb"
}

variable "vultr_os_id" {
  description = "Vultr OS ID (2625 = Debian 13)."
  type        = number
  default     = 2625
}

variable "bootstrap_ssh_public_key" {
  description = "OpenSSH public key registered as a vultr_ssh_key and injected into root at first boot. Required when create_node = true."
  type        = string
  default     = null
}

variable "operator_ssh_public_keys" {
  description = "Public keys for the `deploy` user (scripts/deploy). Defaults to [bootstrap_ssh_public_key]."
  type        = list(string)
  default     = null
}

variable "unicast_v6_block" {
  description = "This POP's /64 from the operator /48 (address-plan.yaml). null = none."
  type        = string
  default     = null
}

variable "wg_address" {
  description = "This POP's WireGuard address/prefix (address-plan.yaml). null = no mesh."
  type        = string
  default     = null
}

variable "cluster_enabled" {
  description = "Enable forge replication on the dev node."
  type        = bool
  default     = false
}
