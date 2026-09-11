variable "name" {
  description = "POP name, e.g. \"ewr1\". Used as the instance label default, in tags and in the hostname default."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.name))
    error_message = "name must be a lower-case DNS label (it becomes <name>.nodes.<zone>)."
  }
}

variable "region" {
  description = "Vultr region ID (ewr, fra, sgp, ...). See infra/providers/vultr/facts.yaml for the list and transit-filtering notes; prefer dynamic-filter regions for the first wave."
  type        = string

  validation {
    condition     = can(regex("^[a-z]{3}$", var.region))
    error_message = "region must be a three-letter Vultr region code."
  }
}

variable "plan" {
  description = "Vultr plan ID. vc2-1c-1gb ($5/mo, 1 TB) is the floor for BGP + BIRD + forge; vhp-1c-1gb-intel ($6/mo, 2 TB, NVMe) for production."
  type        = string
  default     = "vc2-1c-1gb"
}

variable "os_id" {
  description = "Vultr OS image ID. Default 2625 = Debian 13 (infra/providers/vultr/facts.yaml, verified against api.vultr.com/v2/os on 2026-09-10). Debian 12 = 2136."
  type        = number
  default     = 2625
}

variable "ssh_key_ids" {
  description = "Vultr SSH key IDs installed into root's authorized_keys at first boot (the bootstrap key; see ../../environments/dev/vultr.tf for the vultr_ssh_key resource)."
  type        = list(string)
  default     = []
}

variable "enable_ipv6" {
  description = "Attach a /64 to the instance. Required: the IPv6 BGP session and the WireGuard mesh run over the provider's IPv6."
  type        = bool
  default     = true
}

variable "firewall_group_id" {
  description = "Existing Vultr firewall group ID to attach. Ignored when create_firewall = true. null = no Vultr firewall (host nftables still applies)."
  type        = string
  default     = null
}

variable "create_firewall" {
  description = "Create a vultr_firewall_group with the forge rule set (TCP 22/1965/2200, UDP 51820, ICMP v4/v6 from anywhere) and attach it. See README for what the Vultr firewall does and does not cover."
  type        = bool
  default     = true
}

variable "extra_firewall_rules" {
  description = "Additional inbound rules for the created firewall group, keyed by a stable name. protocol: tcp|udp|icmp|gre|esp|ah; ip_type: v4|v6; port: \"22\" or \"8000:9000\" (tcp/udp only); subnet + subnet_size: source address and prefix length as separate fields (0.0.0.0 + 0 or :: + 0 for anywhere; the provider rejects CIDR notation)."
  type = map(object({
    protocol    = string
    ip_type     = string
    subnet      = string
    subnet_size = number
    port        = optional(string)
    notes       = optional(string)
  }))
  default = {}
}

variable "user_data" {
  description = "cloud-init user data (plain text; the provider base64-encodes it for the API). Produced by modules/forge-node."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to the instance (and firewall group description)."
  type        = list(string)
  default     = []
}

variable "backups" {
  description = "Vultr automatic backups: \"enabled\" or \"disabled\". Costs +20% of the plan; forge-backup.timer plus object storage is the intended path, so default disabled."
  type        = string
  default     = "disabled"

  validation {
    condition     = contains(["enabled", "disabled"], var.backups)
    error_message = "backups must be \"enabled\" or \"disabled\"."
  }
}

variable "hostname" {
  description = "Instance hostname. Changing it forces a reinstall in the Vultr API, so set it once. Defaults to the POP name."
  type        = string
  default     = null
}

variable "label" {
  description = "Human-readable label shown in the Vultr console. Defaults to the POP name."
  type        = string
  default     = null
}

variable "vpc_ids" {
  description = "Optional Vultr VPC IDs (region-bound, IPv4 only). Not used by default: cross-region traffic goes over WireGuard on public IPv6."
  type        = list(string)
  default     = []
}
