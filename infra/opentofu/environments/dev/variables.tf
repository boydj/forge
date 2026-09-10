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
  description = "Anycast IPv4 addresses for git.<zone>."
  type        = list(string)
}

variable "anycast_v6" {
  description = "Anycast IPv6 addresses for git.<zone>."
  type        = list(string)
}

variable "nodes" {
  description = "POP name => unicast addresses."
  type = map(object({
    ipv4 = optional(string)
    ipv6 = optional(string)
  }))
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
