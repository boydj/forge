# Database API key (RIPE DB > My Account > API keys, scoped to JOSHBOYD-MNT,
# format KEYID:SECRET). Supplied as TF_VAR_ripe_api_key by
#   eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"
# Never written to disk here.
provider "ripedb" {
  api_key = var.ripe_api_key
  # First run with dry_run = true (default): the RIPE DB validates syntax,
  # authorisation and references without changing anything.
  dry_run         = var.dry_run
  exit_on_warning = false
}
