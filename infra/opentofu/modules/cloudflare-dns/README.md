# cloudflare-dns

Reusable OpenTofu module that publishes the forge's DNS records into an
existing Cloudflare zone:

| Record | Name | Source |
|---|---|---|
| A / AAAA | `<service_hostname>.<zone_name>` (default `git.<zone>`) | `anycast_v4` / `anycast_v6` |
| SSHFP | same | `sshfp` |
| A / AAAA | `<pop>.<nodes_subdomain>.<zone_name>` (default `<pop>.nodes.<zone>`) | `nodes` |
| any | caller-defined | `extra_records` |
| DNSSEC | zone | `manage_dnssec = true` |

Everything is `proxied = false`. Gemini (TCP/1965) and SSH (TCP/22) are not
HTTP and not on Cloudflare's proxied port list, so orange-clouding any of these
names would break the service. The module never creates the zone; it looks it
up with the `cloudflare_zones` data source (or takes `zone_id` directly).

Provider: `cloudflare/cloudflare ~> 5.24` (v5 schema: `cloudflare_dns_record`,
`content`, nested `data = {}`); validated with OpenTofu 1.12.6.

## Usage

```hcl
module "dns" {
  source = "../../modules/cloudflare-dns"

  zone_name        = "as215520.net"
  service_hostname = "git"

  anycast_v4 = ["203.0.113.1"]
  anycast_v6 = ["2001:db8:215:520::1"]

  nodes = {
    ewr1 = { ipv4 = "203.0.113.10", ipv6 = "2001:db8:215:520:1::10" }
    fra1 = { ipv6 = "2001:db8:215:520:2::10" } # v6-only POP
  }

  # ssh-keygen -r git.as215520.net -f /etc/ssh/ssh_host_ed25519_key.pub
  sshfp = [
    { algorithm = 4, type = 2, fingerprint = "0123...cdef" },
  ]

  extra_records = {
    caa_issue = {
      name = "@"
      type = "CAA"
      data = { flags = 0, tag = "issue", value = ";" }
    }
    spf = { name = "@", type = "TXT", content = "v=spf1 -all" }
  }

  manage_dnssec = true
}
```

## Inputs

| Name | Type | Default | Notes |
|---|---|---|---|
| `zone_name` | string | required | Existing zone apex. |
| `zone_id` | string | `null` | Skip the lookup; token then needs only `Zone:DNS:Edit`. |
| `account_id` | string | `null` | Narrow the lookup when the token spans accounts. |
| `service_hostname` | string | `"git"` | `"@"` for apex. |
| `anycast_v4` / `anycast_v6` | list(string) | `[]` | One record per address. |
| `nodes` | map(object) | `{}` | `{ ipv4 = optional, ipv6 = optional }` per POP. |
| `nodes_subdomain` | string | `"nodes"` | |
| `sshfp` | list(object) | `[]` | `{ algorithm, type, fingerprint }` numeric alg/type. |
| `extra_records` | map(object) | `{}` | `{ name, type, content?, data?, ttl?, priority?, comment? }`. |
| `ttl` | number | `300` | Service records. 60-86400 (30 on Enterprise). |
| `node_ttl` | number | `= ttl` | Lower to 60 while renumbering a POP. |
| `manage_dnssec` | bool | `false` | Creates `cloudflare_zone_dnssec` with `status = "active"`. |
| `comment` | string | managed-by string | Stamped on every record. |

## Outputs

`zone_id`, `service_fqdn`, `service_urls` (`gemini://`, `git@`), `node_fqdns`,
`service_record_ids`, `sshfp_record_ids`, `node_record_ids`,
`extra_record_ids`, `dnssec_ds`.

## Token scopes

* `Zone → DNS → Edit` on the zone (always).
* `Zone → Zone → Read` on the zone (only when `zone_id` is not supplied).
* DNSSEC is managed through the DNS permission; no extra scope is needed.

Supply it via `CLOUDFLARE_API_TOKEN` in the environment; never put it in a
committed `.tfvars`. See `environments/dev/README.md`.

## Why TTL 300 and not 60

Anycast addresses are the stable identity of the service; failover between
POPs is a BGP event that DNS never sees. A 60 s TTL therefore buys nothing on
the anycast names and only multiplies resolver traffic. 300 is the same value
Cloudflare uses for "Auto". The unicast node names are the only records whose
*values* change (renumbering a POP), and `node_ttl` exists for lowering those
ahead of a planned change.

## Generating SSHFP input

On the host that owns the SSH host key (the key must be identical on every POP
because they share one anycast address):

```sh
ssh-keygen -r git.as215520.net -f /etc/ssh/ssh_host_ed25519_key.pub
# git.as215520.net IN SSHFP 4 1 <sha1>
# git.as215520.net IN SSHFP 4 2 <sha256>
```

Publish only the `type = 2` (SHA-256) lines. Ed25519 is algorithm 4, ECDSA 3,
RSA 1.
