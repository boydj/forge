variable "ripe_api_key" {
  description = "RIPE Database API key, KEYID:SECRET. From the SOPS bundle (ripe_db_api_key)."
  type        = string
  sensitive   = true
}

variable "dry_run" {
  description = "true = validate against the RIPE DB without writing (default). Set false to apply for real."
  type        = bool
  default     = true
}
