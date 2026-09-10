variable "zone_name" {
  description = "Apex name of the Cloudflare zone, e.g. \"as215520.net\". The zone must already exist in the account the API token can see; this module never creates or deletes zones."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9-]+\\.)+[a-z0-9-]+$", var.zone_name))
    error_message = "zone_name must be a lower-case DNS name without a trailing dot."
  }
}

variable "zone_id" {
  description = "Optional Cloudflare zone ID. When set, the cloudflare_zones lookup is skipped, which lets the API token get by with only Zone:DNS:Edit (no Zone:Zone:Read)."
  type        = string
  default     = null
}

variable "account_id" {
  description = "Optional Cloudflare account ID used to narrow the zone lookup when the token can see zones in several accounts."
  type        = string
  default     = null
}

variable "service_hostname" {
  description = "Host label for the anycast service inside the zone, e.g. \"git\" -> git.<zone_name>. Use \"@\" for the zone apex."
  type        = string
  default     = "git"
}

variable "anycast_v4" {
  description = "IPv4 addresses announced from every POP (anycast). One A record is created per address."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for ip in var.anycast_v4 : can(cidrhost("${ip}/32", 0))])
    error_message = "Every anycast_v4 entry must be a plain IPv4 address."
  }
}

variable "anycast_v6" {
  description = "IPv6 addresses announced from every POP (anycast). One AAAA record is created per address. Supply them in RFC 5952 compressed lower-case form to avoid perpetual diffs against Cloudflare's normalised value."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for ip in var.anycast_v6 : can(cidrhost("${ip}/128", 0))])
    error_message = "Every anycast_v6 entry must be a plain IPv6 address."
  }
}

variable "nodes" {
  description = "Per-POP unicast addresses keyed by POP name, e.g. { ewr1 = { ipv4 = \"203.0.113.10\", ipv6 = \"2001:db8::10\" } }. Each becomes <name>.<nodes_subdomain>.<zone_name>. Either address may be omitted."
  type = map(object({
    ipv4 = optional(string)
    ipv6 = optional(string)
  }))
  default = {}

  validation {
    condition     = alltrue([for k, v in var.nodes : can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", k))])
    error_message = "Node names must be valid lower-case DNS labels."
  }
}

variable "nodes_subdomain" {
  description = "Label under which per-node records live, giving <node>.<nodes_subdomain>.<zone_name>."
  type        = string
  default     = "nodes"
}

variable "sshfp" {
  description = "SSHFP records (RFC 4255) for the service hostname. Generate with `ssh-keygen -r <host> -f /etc/ssh/ssh_host_ed25519_key.pub`. algorithm: 1=RSA 2=DSA 3=ECDSA 4=Ed25519 6=Ed448; type: 1=SHA-1 2=SHA-256."
  type = list(object({
    algorithm   = number
    type        = number
    fingerprint = string
  }))
  default = []

  validation {
    condition = alltrue([
      for r in var.sshfp :
      contains([1, 2, 3, 4, 6], r.algorithm) && contains([1, 2], r.type) && can(regex("^[0-9A-Fa-f]+$", r.fingerprint))
    ])
    error_message = "sshfp entries need algorithm in {1,2,3,4,6}, type in {1,2} and a hex fingerprint."
  }
}

variable "extra_records" {
  description = "Additional records keyed by a stable identifier (used as the for_each key). `name` is relative to the zone (\"@\" for apex, \"_dmarc\" for _dmarc.<zone>) or a FQDN ending in the zone name. Use `content` for simple types (TXT, CNAME, MX, ...) and `data` for structured ones (CAA, SRV, TLSA, ...)."
  type = map(object({
    name     = string
    type     = string
    content  = optional(string)
    ttl      = optional(number)
    priority = optional(number)
    comment  = optional(string)
    # Structured RDATA for CAA / SSHFP / SRV / TLSA / URI / DS style records.
    # Typed explicitly (not `any`) so entries with and without `data` unify.
    data = optional(object({
      flags         = optional(number) # CAA, DS
      tag           = optional(string) # CAA
      value         = optional(string) # CAA
      algorithm     = optional(number) # SSHFP, DS, TLSA, DNSKEY
      type          = optional(number) # SSHFP
      fingerprint   = optional(string) # SSHFP
      priority      = optional(number) # SRV, URI
      weight        = optional(number) # SRV, URI
      port          = optional(number) # SRV
      target        = optional(string) # SRV, URI
      usage         = optional(number) # TLSA, SMIMEA
      selector      = optional(number) # TLSA, SMIMEA
      matching_type = optional(number) # TLSA, SMIMEA
      certificate   = optional(string) # TLSA, SMIMEA
      key_tag       = optional(number) # DS
      digest_type   = optional(number) # DS
      digest        = optional(string) # DS
    }))
  }))
  default = {}
}

variable "ttl" {
  description = "TTL in seconds for the service (anycast) A/AAAA and SSHFP records. Cloudflare allows 60-86400 for DNS-only records (30 on Enterprise). 300 is deliberate: anycast failover happens in BGP, not DNS, so a low TTL only adds resolver load."
  type        = number
  default     = 300

  validation {
    condition     = var.ttl >= 30 && var.ttl <= 86400
    error_message = "ttl must be between 30 and 86400 seconds (60 minimum on non-Enterprise zones)."
  }
}

variable "node_ttl" {
  description = "TTL for per-node unicast records. Defaults to `ttl`; lower it (60) temporarily while renumbering a POP."
  type        = number
  default     = null
}

variable "manage_dnssec" {
  description = "When true, manage the zone's DNSSEC state (status = active) through cloudflare_zone_dnssec. The DS record output must then be published at the registrar."
  type        = bool
  default     = false
}

variable "comment" {
  description = "Comment stamped on every record so hand-made records are distinguishable from managed ones."
  type        = string
  default     = "managed by opentofu (forge/cloudflare-dns)"
}
