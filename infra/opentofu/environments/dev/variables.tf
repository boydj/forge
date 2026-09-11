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





variable "pops" {
  description = "POPs of this environment, keyed by name (values from infra/network/address-plan.yaml). Every POP gets a Vultr instance (when create_node), DNS under nodes.<zone>, and a rendered node config."
  type = map(object({
    region           = string
    plan             = optional(string, "vc2-1c-1gb")
    index            = number
    unicast_v6_block = string
    wg_address       = string # address/prefix, e.g. "fda5:bc65:9bb1:1::1/64"
    cluster_enabled  = optional(bool, true)
    bgp_announce     = optional(bool, false)
    role             = optional(string, "replica")
  }))
  default = {}
}

variable "monitors" {
  description = "Monitoring hosts of this environment, keyed by name (address-plan.yaml roles [monitor]; normally just mon1). Each gets a modules/vultr-monitor instance (when create_node), DNS under nodes.<zone> on its provider addresses, and a rendered prometheus.yml for every created POP (monitor.tf)."
  type = map(object({
    region               = string
    plan                 = optional(string, "vc2-1c-1gb")
    index                = number
    wg_address           = string # address/prefix, e.g. "fda5:bc65:9bb1:1::fa/64"
    alertmanager_targets = optional(list(string), [])
  }))
  default = {}
}

variable "metadata_leader" {
  description = "Node that owns users, certificates and SSH keys (cluster.metadata_leader)."
  type        = string
  default     = ""
}

variable "control_port" {
  description = "Cluster control port over WireGuard."
  type        = number
  default     = 9200
}

variable "docs_repo" {
  description = "Repository whose docs/ is the documentation site at /docs/ (the forge dogfoods its own source)."
  type        = string
  default     = "jdb/forge"
}
