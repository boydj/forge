# Off-site backups on Backblaze B2 (docs/disaster-recovery.md, "Off-site").
#
# One private bucket with versioning (B2 keeps every version) and a
# lifecycle rule that hides objects 30 days after upload and deletes hidden
# versions a day later, plus one application key restricted to that bucket
# that can list and write but NOT delete: a node (or whoever steals its
# key) cannot destroy history. Retention is therefore the lifecycle rule,
# not the writer.
#
# Credentials: the provider reads B2_APPLICATION_KEY_ID / B2_APPLICATION_KEY
# from the environment (`scripts/secrets env` exports them from
# b2_admin_key_id / b2_admin_key in the bundle). The writer key it creates
# goes into the bundle as b2_backup_key_id / b2_backup_key
# (docs/runbooks/enable-offsite-backups.md).

terraform {
  required_version = ">= 1.6"
  required_providers {
    b2 = {
      source  = "Backblaze/b2"
      version = "~> 0.10"
    }
  }
}

provider "b2" {}

variable "bucket_name" {
  description = "Globally unique B2 bucket name (6-63 chars, letters, digits, hyphens)."
  type        = string
  default     = "as215520-forge-backups"
}

variable "retention_days" {
  description = "Days an archive stays visible after upload; hidden versions are deleted a day later."
  type        = number
  default     = 30
}

resource "b2_bucket" "backups" {
  bucket_name = var.bucket_name
  bucket_type = "allPrivate"

  lifecycle_rules {
    file_name_prefix              = ""
    days_from_uploading_to_hiding = var.retention_days
    days_from_hiding_to_deleting  = 1
  }

  default_server_side_encryption {
    mode      = "SSE-B2"
    algorithm = "AES256"
  }
}

# Write-only key for the nodes: list (rclone resolves the bucket), write.
# No deleteFiles, no readFiles: a compromised node cannot read other nodes'
# archives nor delete anything.
resource "b2_application_key" "writer" {
  key_name     = "forge-backup-writer"
  bucket_id    = b2_bucket.backups.bucket_id
  capabilities = ["listBuckets", "listFiles", "writeFiles"]
}

output "bucket" {
  value = b2_bucket.backups.bucket_name
}

output "writer_key_id" {
  value = b2_application_key.writer.application_key_id
}

output "writer_key" {
  value     = b2_application_key.writer.application_key
  sensitive = true
}
