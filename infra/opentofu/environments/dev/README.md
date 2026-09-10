# environments/dev

Root module for one development POP (`ewr1`): DNS (`modules/cloudflare-dns`),
node definition (`modules/forge-node`) and the Vultr instance
(`modules/vultr-pop`). Each half is toggled independently:

| Toggle | Default | Needs | Effect |
|---|---|---|---|
| `create_dns` | `true` | `CLOUDFLARE_API_TOKEN` | `git.<zone>` A/AAAA/SSHFP, `<pop>.nodes.<zone>` |
| `create_node` | `false` | `VULTR_API_KEY`, `bootstrap_ssh_public_key` | `vultr_ssh_key` + firewall group + instance with the rendered cloud-init |

With `create_node = true` the instance's provider addresses are merged into
the DNS `nodes` map, so `ewr1.nodes.<zone>` follows the instance. The
`forge-node` module is always evaluated (it only renders templates), so
`tofu output -raw cloud_init` works without any credentials.

## Files

| File | Purpose |
|---|---|
| `versions.tf` | OpenTofu + provider pins (cloudflare ~> 5.24, vultr ~> 2.32). |
| `providers.tf` | Empty provider blocks; credentials come from the environment. |
| `variables.tf` | Inputs: addresses, nodes, SSHFP, toggles, POP settings. |
| `dns.tf` | cloudflare-dns module call and outputs. |
| `node.tf` | forge-node module call (`cloud_init` output). |
| `vultr.tf` | bootstrap `vultr_ssh_key`, vultr-pop module, `pop` output. |
| `dev.auto.tfvars.example` | Placeholder values. Copy to `dev.auto.tfvars` (git-ignored). |

## Credentials

Both providers read the environment: `CLOUDFLARE_API_TOKEN` and
`VULTR_API_KEY`. They live SOPS-encrypted in `infra/secrets/dev.enc.yaml`
(schema in `infra/secrets/README.md`); `scripts/secrets env` prints the
`export` lines, so:

```sh
cd infra/opentofu/environments/dev
cp dev.auto.tfvars.example dev.auto.tfvars   # edit with real values
eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
tofu init && tofu plan
tofu apply
```

Cloudflare token: dashboard → My Profile → API Tokens → "Edit zone DNS"
template, `Zone → DNS → Edit` plus `Zone → Zone → Read` (drop Zone Read when
`zone_id` is set), scoped to `as215520.net`.
Vultr key: console → Account → API, enable the API and restrict access to
your source IPs (v4 and v6). It grants full account access; there is no
scoping.

## Order of operations for a new POP

1. `create_dns = true`, `create_node = false`: publish anycast records.
2. Set `bootstrap_ssh_public_key`, `create_node = true`, apply. The instance
   boots with cloud-init (`modules/forge-node/README.md` lists what it does).
   `tofu output pop` prints the addresses and the admin SSH line.
3. `scripts/deploy init-node ewr1` collects the node's age recipient;
   `scripts/deploy ewr1` pushes binary, configs and secrets.
4. Request BGP on the account (console; needs this live instance) and BYOIP
   onboarding, see `docs/research/vultr.md`. Until then the node serves on
   its provider addresses only.

## Validation without credentials

```sh
tofu fmt -check -recursive
tofu init -backend=false
tofu validate
```

## First apply against an existing zone

Records that already exist by hand in the zone must be imported, not
recreated:

```sh
tofu import 'module.dns[0].cloudflare_dns_record.service_a["203.0.113.1"]' <zone_id>/<record_id>
```
