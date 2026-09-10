# Authentication is taken from CLOUDFLARE_API_TOKEN in the environment.
# The token is deliberately not a variable: variables end up in tfvars files,
# plan files and shell history. See README.md for the SOPS workflow.
provider "cloudflare" {}
