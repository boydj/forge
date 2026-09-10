terraform {
  required_version = ">= 1.8.0"

  required_providers {
    vultr = {
      # Resolves on registry.opentofu.org (mirror of registry.terraform.io).
      # Latest at time of writing: v2.32.0 (2026-07-14), re-checked against the
      # registry versions API on 2026-09-10. No BGP/BYOIP resources exist in
      # this provider; those remain console/ticket steps (docs/research/vultr.md).
      source  = "vultr/vultr"
      version = "~> 2.32"
    }
  }
}
