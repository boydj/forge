# Authentication is taken from the environment:
#   CLOUDFLARE_API_TOKEN  (cloudflare provider)
#   VULTR_API_KEY         (vultr provider)
# Neither is a variable: variables end up in tfvars files, plan files and
# shell history. `scripts/secrets env infra/secrets/dev.enc.yaml` prints the
# export lines from the SOPS file; see README.md and docs/secrets.md.
provider "cloudflare" {}

# NOTE: the Vultr provider schema marks api_key as required (env default), so
# `tofu validate` in this directory needs VULTR_API_KEY set to *anything*
# (CI uses VULTR_API_KEY=validate-only). No API call is made by validate.
provider "vultr" {
  # 30 req/s account limit; the provider paces itself.
  rate_limit  = 500
  retry_limit = 3
}
