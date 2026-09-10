terraform {
  # No providers: this module only renders templates (templatefile, file),
  # so it validates and plans without any credentials.
  required_version = ">= 1.8.0"
}
