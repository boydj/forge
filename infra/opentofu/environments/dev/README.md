# environments/dev

Root module that publishes DNS for one development POP (`ewr1`) plus the
anycast `git.as215520.net` records, by calling `modules/cloudflare-dns`.

## Files

| File | Purpose |
|---|---|
| `versions.tf` | OpenTofu + provider pins. |
| `providers.tf` | Empty `provider "cloudflare" {}`; credentials come from the environment. |
| `variables.tf` | Inputs (addresses, nodes, SSHFP, DNSSEC toggle). |
| `dns.tf` | Module call and outputs. |
| `dev.auto.tfvars.example` | Placeholder values. Copy to `dev.auto.tfvars`, which is git-ignored. |

## Credentials: `CLOUDFLARE_API_TOKEN`

The Cloudflare provider reads `CLOUDFLARE_API_TOKEN` from the environment.
The token is never a Tofu variable, never in a `.tfvars`, never in state.

Create the token in the Cloudflare dashboard (My Profile → API Tokens →
Create Token → "Edit zone DNS" template) with:

* Permissions: `Zone → DNS → Edit`, `Zone → Zone → Read`
  (Zone Read can be dropped if `zone_id` is set in tfvars)
* Zone Resources: Include → Specific zone → `as215520.net`
* Optionally restrict client IP and set an expiry.

Store it SOPS-encrypted, e.g. `secrets/cloudflare.dev.env`:

```sh
# one-time
printf 'CLOUDFLARE_API_TOKEN=%s\n' "$(cat token.txt)" | sops --encrypt --input-type dotenv --output-type dotenv /dev/stdin > secrets/cloudflare.dev.env
shred -u token.txt
```

and run Tofu inside a shell that only sees the decrypted value:

```sh
cd infra/opentofu/environments/dev
cp dev.auto.tfvars.example dev.auto.tfvars   # edit with real addresses
sops exec-env ../../../../secrets/cloudflare.dev.env 'tofu init && tofu plan'
sops exec-env ../../../../secrets/cloudflare.dev.env 'tofu apply'
```

`sops exec-env` decrypts into the child's environment only; nothing lands on
disk and the token is not in your shell history. Rotate the token in the
dashboard and re-encrypt the file when an operator leaves.

## Validation without credentials

```sh
tofu fmt -check -recursive
tofu init -backend=false
tofu validate
```

`validate` needs the provider binary (init downloads it) but no token and no
network access to Cloudflare.

## First apply against an existing zone

Records that already exist by hand in the zone (for example an `A` for
`git.as215520.net` created in the dashboard) must be imported, not recreated:

```sh
tofu import 'module.dns.cloudflare_dns_record.service_a["203.0.113.1"]' <zone_id>/<record_id>
```

Record IDs: `curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
"https://api.cloudflare.com/client/v4/zones/<zone_id>/dns_records?name=git.as215520.net"`.
