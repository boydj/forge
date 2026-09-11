terraform {
  required_version = ">= 1.8.0"
  required_providers {
    ripedb = {
      source  = "frederic-arr/ripedb"
      version = "~> 0.5"
    }
  }
  # Local state: the objects' truth is the RIPE Database itself; state only
  # records that we manage them. Re-import after a lost state (README).
}
