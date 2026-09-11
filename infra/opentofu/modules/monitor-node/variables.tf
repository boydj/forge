variable "name" {
  description = "Monitor name, e.g. \"mon1\" (address-plan.yaml roles [monitor]). Hostname, and the pop/node labels Prometheus stamps on its own alerts (external_labels)."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.name))
    error_message = "name must be a lower-case DNS label."
  }
}

variable "hostname_fqdn" {
  description = "Host FQDN, e.g. \"mon1.nodes.as215520.net\"."
  type        = string
}

variable "service_hostname" {
  description = "Public service name the Gemini probes send, e.g. \"git.as215520.net\" (anycast)."
  type        = string
  default     = "git.as215520.net"
}

variable "pop_index" {
  description = "The monitor's index in infra/network/address-plan.yaml (drives its WireGuard address). Informational here."
  type        = number
  default     = 250
}

variable "wg_address" {
  description = "WireGuard mesh address with prefix length, e.g. \"fda5:bc65:9bb1:1::fa/64\" (address-plan.yaml). Prometheus scrapes the POPs from here."
  type        = string
  validation {
    condition     = can(cidrhost(var.wg_address, 0))
    error_message = "wg_address must be an address with prefix length, e.g. fda5::fa/64."
  }
}

variable "wg_port" {
  description = "WireGuard UDP port."
  type        = number
  default     = 51820
}

variable "admin_ssh_port" {
  description = "Port OpenSSH is moved to."
  type        = number
  default     = 2200
}

variable "operator_ssh_public_keys" {
  description = "Public keys allowed to log in as `deploy` (ssh -L tunnels to Grafana/Prometheus; no sudo). OpenSSH authorized_keys lines."
  type        = list(string)
  default     = []
}

variable "mesh_tcp_ports" {
  description = "TCP ports admitted from wg0 (Prometheus, Grafana). Everything else on the host is loopback-only."
  type        = list(number)
  default     = [9090, 3000]
}

variable "anycast_v4" {
  description = "The anycast IPv4 service address the blackbox probes target (address-plan.yaml ipv4.services[forge])."
  type        = string
  validation {
    condition     = can(cidrhost("${var.anycast_v4}/32", 0))
    error_message = "anycast_v4 must be a plain IPv4 address."
  }
}

variable "anycast_v6" {
  description = "The anycast IPv6 service address the blackbox probes target (address-plan.yaml ipv6.services[forge])."
  type        = string
  validation {
    condition     = can(cidrhost("${var.anycast_v6}/128", 0))
    error_message = "anycast_v6 must be a plain IPv6 address."
  }
}

variable "pops" {
  description = "POPs to scrape and probe (forge role only; the monitor itself and lab are excluded by the caller). wg_address is the bare mesh address, unicast_v6 the POP's /48 node address, provider_* the instance addresses from the provider module. Entries with null provider addresses (instance not created) are dropped."
  type = list(object({
    name          = string
    wg_address    = string
    unicast_v6    = string
    provider_ipv4 = optional(string)
    provider_ipv6 = optional(string)
  }))
  default = []
}

variable "blackbox_address" {
  description = "Where blackbox_exporter listens on the monitor host (loopback)."
  type        = string
  default     = "127.0.0.1:9115"
}

variable "alertmanager_targets" {
  description = "Alertmanager host:port targets. Empty = no Alertmanager (alerts stay visible in the Prometheus UI until a destination is chosen, docs/monitoring.md)."
  type        = list(string)
  default     = []
}
