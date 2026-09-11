variable "name" {
  description = "Monitor name, e.g. \"mon1\" (infra/network/address-plan.yaml, roles [monitor]). Becomes the hostname and <name>.nodes.<zone>."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.name))
    error_message = "name must be a lower-case DNS label."
  }
}

variable "region" {
  description = "Vultr region ID. Pick one without a forge POP (e.g. ord) so the blackbox probes of the anycast addresses cross real transit paths instead of a local hop."
  type        = string
  validation {
    condition     = can(regex("^[a-z]{3}$", var.region))
    error_message = "region must be a three-letter Vultr region code."
  }
}

variable "plan" {
  description = "Vultr plan ID. vc2-1c-1gb ($5/mo, 25 GB) fits Prometheus + Grafana at this cardinality (docs/monitoring.md); vhp-1c-1gb-intel ($6/mo) for NVMe."
  type        = string
  default     = "vc2-1c-1gb"
}

variable "os_id" {
  description = "Vultr OS image ID. Default 2625 = Debian 13 (infra/providers/vultr/facts.yaml)."
  type        = number
  default     = 2625
}

variable "ssh_key_ids" {
  description = "Vultr SSH key IDs installed into root's authorized_keys at first boot (the bootstrap key `scripts/deploy monitor` uses as root)."
  type        = list(string)
  default     = []
}

variable "admin_ssh_port" {
  description = "Port OpenSSH listens on (cloud-init moves it there); opened in the Vultr firewall."
  type        = number
  default     = 2200
}

variable "wg_port" {
  description = "WireGuard UDP port opened in the Vultr firewall."
  type        = number
  default     = 51820
}

variable "firewall_group_id" {
  description = "Existing Vultr firewall group ID to attach. Ignored when create_firewall = true. null = no Vultr firewall (host nftables still applies)."
  type        = string
  default     = null
}

variable "create_firewall" {
  description = "Create a vultr_firewall_group with the monitor rule set (TCP admin SSH, UDP WireGuard, ICMP v4/v6 from anywhere) and attach it."
  type        = bool
  default     = true
}

variable "extra_firewall_rules" {
  description = "Additional inbound rules for the created firewall group, keyed by a stable name (same shape as modules/vultr-pop)."
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
  description = "cloud-init user data (plain text). Produced by modules/monitor-node."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to the instance."
  type        = list(string)
  default     = []
}

variable "backups" {
  description = "Vultr automatic backups: \"enabled\" or \"disabled\". Prometheus data is reproducible from the POPs within its retention, so default disabled."
  type        = string
  default     = "disabled"
  validation {
    condition     = contains(["enabled", "disabled"], var.backups)
    error_message = "backups must be \"enabled\" or \"disabled\"."
  }
}

variable "hostname" {
  description = "Instance hostname. Changing it forces a reinstall in the Vultr API, so set it once. Defaults to the monitor name."
  type        = string
  default     = null
}

variable "label" {
  description = "Human-readable label shown in the Vultr console. Defaults to the monitor name."
  type        = string
  default     = null
}
