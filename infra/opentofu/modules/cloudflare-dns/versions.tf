terraform {
  # OpenTofu >= 1.8 is required for optional() object attributes with defaults
  # and for provider-defined functions; 1.12.x is what this module was
  # validated with. The `terraform` block name is kept for compatibility:
  # OpenTofu reads it unchanged.
  required_version = ">= 1.8.0"

  required_providers {
    cloudflare = {
      # Resolves on both registry.opentofu.org and registry.terraform.io.
      source = "cloudflare/cloudflare"
      # v5.x uses a completely different schema from v4.x
      # (cloudflare_dns_record, `content`, nested `data = {}` attribute).
      # Latest at time of writing: v5.24.0 (2026-08-24).
      version = "~> 5.24"
    }
  }
}
