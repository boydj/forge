terraform {
  required_version = ">= 1.8.0"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.24"
    }
  }

  # No backend yet: state is local while the dev environment is scaffolded.
  # Move to a remote backend (S3-compatible + locking, or the like) before a
  # second operator touches this; DNS record IDs live in state.
}
