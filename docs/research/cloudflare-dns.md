# Cloudflare DNS-as-code for the forge

Research notes and design for hosting `as215520.net` DNS on Cloudflare and
managing it with OpenTofu. Verified against live documentation on 2026-09-10.

Service names:

| Name | Purpose | Records |
|---|---|---|
| `git.as215520.net` | anycast Gemini (`gemini://git.as215520.net/`, TCP/1965) and SSH (`git@git.as215520.net`, TCP/22) | A, AAAA, SSHFP |
| `<pop>.nodes.as215520.net` (e.g. `ewr1.nodes.as215520.net`) | per-POP unicast address for management, monitoring, per-node testing | A, AAAA |
| `as215520.net` (apex) | later: CAA, TXT (SPF/verification), MX if ever needed | via `extra_records` |

## 1. Findings

### 1.1 Cloudflare Terraform provider v5 (verified)

* Latest release: **v5.24.0** (2026-08-24). A v4 maintenance line still gets
  patches (v4.52.9, 2026-09-01) but has a different schema; do not mix.
  Pin `~> 5.24`.
* Provider source `cloudflare/cloudflare` resolves on **registry.opentofu.org**
  (5.24.0 with linux/amd64 present) as well as on registry.terraform.io.
  `tofu init` recorded `registry.opentofu.org/cloudflare/cloudflare 5.24.0`.
* Authentication: `api_token` (sensitive) or env `CLOUDFLARE_API_TOKEN`.
  Only one auth method may be set. `api_key`/`email` is legacy.

`cloudflare_dns_record` (replaces v4 `cloudflare_record`):

| Attribute | v5 | Notes |
|---|---|---|
| `zone_id` | required string | |
| `name` | required string | The API stores/returns the FQDN; the module always writes FQDNs so plans stay clean. |
| `type` | required string | A, AAAA, CNAME, MX, NS, OPENPGPKEY, PTR, TXT, CAA, CERT, DNSKEY, DS, HTTPS, LOC, NAPTR, SMIMEA, SRV, SSHFP, SVCB, TLSA, URI |
| `ttl` | **required** number | 60-86400 (30 on Enterprise); `1` = Auto, only meaningful for proxied records. |
| `content` | optional string | Was `value` in v4. Used for A/AAAA/CNAME/TXT/MX/NS/PTR. |
| `data` | optional **nested attribute** `data = { ... }` | Was a block `data { }` in v4. Fields are typed: `algorithm`, `type`, `priority`, `weight`, `port`, `key_tag`, ... are numbers (Float64); `fingerprint`, `tag`, `value`, `target`, `certificate`, `digest` strings; `flags` is dynamic (number for CAA). |
| `proxied` | optional bool, default false | Only A/AAAA/CNAME are proxiable at all. |
| `comment`, `tags` | optional | `tags` is also computed. |
| `priority` | optional number | MX/SRV/URI. |
| `settings` | nested attribute | `ipv4_only`, `ipv6_only`, `flatten_cname`; irrelevant unproxied. |

SSHFP example that plans correctly with 5.24.0:

```hcl
resource "cloudflare_dns_record" "sshfp" {
  zone_id = local.zone_id
  name    = "git.as215520.net"
  type    = "SSHFP"
  ttl     = 300
  proxied = false
  data = {
    algorithm   = 4      # Ed25519
    type        = 2      # SHA-256
    fingerprint = "ABCD..."
  }
}
```

Zone lookups:

* `data "cloudflare_zones"` takes a plain `name = "as215520.net"` (plus optional
  `account = { id = ... }`, `status`, `type`, `match`, `order`, `direction`,
  `max_items`) and exposes `result[*].id`, `name`, `name_servers`, `status`.
  (v4 used a `filter { name = ... }` block; v5 does not.)
* `data "cloudflare_zone"` takes `zone_id` directly, or `filter = { name = ... }`.
* `resource "cloudflare_zone"` in v5 uses `name` (not `zone`) and
  `account = { id = ... }` (not `account_id`); `jump_start` is gone.

Other v5 renames relevant here: `cloudflare_zone_dnssec` keeps its name;
arguments `zone_id`, `status` ("active"/"disabled"), `dnssec_multi_signer`,
`dnssec_presigned`, `dnssec_use_nsec3`; computed `ds`, `digest`,
`digest_type`, `algorithm`, `key_tag`, `public_key`, `flags`. Cloudflare's
official HCL migration tool is `tf-migrate`.

### 1.2 OpenTofu

OpenTofu 1.12.6 (latest, 2026-08-19) installed to `~/.local/bin/tofu` from
the GitHub release zip with SHA256SUMS verified. Terraform-syntax HCL
(`terraform {}` block, `required_providers`) is read unchanged; providers are
fetched from registry.opentofu.org, which mirrors `cloudflare/cloudflare`.
Nothing in the module is Terraform-only.

### 1.3 API token scopes

Cloudflare's "Edit zone DNS" token template grants `Zone → DNS → Edit`
(the API calls it "DNS Write"). The `cloudflare_zones` lookup additionally
needs `Zone → Zone → Read`. `cloudflare_zone_dnssec` needs DNS Read/Write
only. Scope the token to the single zone `as215520.net`; optional client-IP
filter and expiry. The secret is shown once; see the dev README for SOPS
handling. (The full Zone permission table on the permissions reference page
was truncated when fetched; the names above match the dashboard labels and
the token-template page.)

### 1.4 Does Cloudflare offer anything for Gemini? No.

* The proxy carries only HTTP(S) on ports 80, 8080, 8880, 2052, 2082, 2086,
  2095 and 443, 2053, 2083, 2087, 2096, 8443. Cloudflare's own docs: for an
  SSH server on 22 you must either grey-cloud the record or use Spectrum.
* Spectrum "for all TCP and UDP ports is only available on the Enterprise
  plan", and it would terminate the connection at Cloudflare's edge, which
  is the exact thing an anycast origin is meant to be.
* Gemini's trust model is TOFU on a self-signed certificate; an intermediary
  would have to hold the private key.

So every record is `proxied = false` ("DNS only"). Cloudflare is used purely
as an authoritative DNS host: anycast nameservers, DNSSEC signing, API.

### 1.5 SSHFP (RFC 4255)

* RDATA: algorithm (1 octet), fingerprint type (1 octet), fingerprint (hex).
  IANA: algorithms RSA=1, DSA=2, ECDSA=3, Ed25519=4, Ed448=6; fingerprint
  types SHA-1=1, SHA-256=2.
* RFC 4255: a key "MUST NOT be trusted if the SSHFP resource record (RR) used
  for verification was not authenticated by a trusted SIG RR" - i.e. SSHFP
  is only useful with DNSSEC and a validating resolver.
* OpenSSH `VerifyHostKeyDNS`: `yes` implicitly trusts keys matching a
  *secure* (DNSSEC-validated, AD bit) fingerprint; insecure matches degrade
  to `ask`; default `no`. Clients need a validating resolver (or
  systemd-resolved/unbound with DNSSEC on) for `yes` to take effect.
* Generate on a host holding the key: `ssh-keygen -r git.as215520.net -f
  /etc/ssh/ssh_host_ed25519_key.pub` prints both SHA-1 and SHA-256 lines
  (`-O hashalg=sha256` to restrict; `-g` for generic RDATA form). Publish
  type 2 only.
* Operational consequence: because all POPs answer for one anycast address,
  **every POP must carry the same SSH host key**, distributed out of band
  (SOPS). Different keys per POP would make SSHFP - and known_hosts - flap.
  Per-node names `ewr1.nodes...` may carry their own SSHFP if per-node SSH is
  wanted; the module only publishes SSHFP for the service name.
* Cloudflare stores the fingerprint upper-case; the module `upper()`s input.

### 1.6 DNSSEC on Cloudflare

* Enabled per zone (dashboard "Enable DNSSEC" or `cloudflare_zone_dnssec`
  with `status = "active"`). Cloudflare signs with ECDSA P-256/SHA-256
  (algorithm 13). The resource exposes the DS record which **must be placed at
  the registrar**; Cloudflare Registrar and `.ch`/`.cz` do it automatically
  via CDS/CDNSKEY scanning (1-2 days).
* `dnssec_multi_signer` / `dnssec_presigned` are for multi-provider or
  externally signed zones; leave `false` for a single-provider zone.
  `dnssec_use_nsec3` optional; default off is fine.
* Ordering for the migration of an already-signed zone: remove old DS, wait
  TTL, move NS, enable on Cloudflare, publish new DS. For an unsigned zone
  just enable and publish DS.
* Until the DS is live, SSHFP records are "insecure" to ssh.

### 1.7 CAA

Not needed for Gemini (self-signed, no CA). Notes for completeness:

* With no CAA record any CA may issue. If Universal SSL is ever active on a
  proxied hostname, Cloudflare auto-adds CAA for its partner CAs
  (Google Trust Services, Let's Encrypt, SSL.com, Sectigo) alongside yours;
  this zone has no proxied records so nothing is auto-added.
* To forbid all issuance (defensive, since nothing on this zone should get a
  WebPKI cert): `CAA 0 issue ";"` at the apex, plus `iodef`. Expressed via
  `extra_records` with `data = { flags = 0, tag = "issue", value = ";" }`.
  Relax with a real CA later if a WebPKI-facing service is added.

### 1.8 TXT and other records for later

`extra_records` covers them. Likely candidates: `v=spf1 -all` and a null
`_dmarc` policy at the apex (domain sends no mail), site-verification TXTs,
and `_forge.<zone>` TXT for service discovery/metadata. TXT `content` must
be quoted; Cloudflare adds quotes if missing (module passes through).

### 1.9 zone_id sourcing and not managing the zone

Three options were considered:

1. `resource "cloudflare_zone"` + `tofu import` - puts the zone under state
   control; a `destroy` or a bad refactor can delete the zone and all
   records. Rejected for now.
2. `data "cloudflare_zones" { name = ... }` - read-only, needs
   `Zone:Zone:Read`, fails loudly (postcondition) if 0 or >1 zones match.
   **Chosen default.**
3. Pass `zone_id` as a variable - no lookup, token needs only `Zone:DNS:Edit`.
   **Supported as an override** (also handy for CI tokens).

If the zone does not exist yet, create it once by hand (or a separate
`zones` root module that is applied rarely) and point NS at the assigned
Cloudflare nameservers at the registrar.

### 1.10 Reverse DNS (verified)

Cloudflare supports reverse zones: you create a zone named e.g.
`113.0.203.in-addr.arpa` or `0.2.5.1.2.8.b.d.0.1.0.0.2.ip6.arpa`, add PTR
records (Free plan if < 200 PTRs), and then "add the two Cloudflare
nameservers provided for the zone at your Regional Internet Registry (RIR)".
Documented examples cover IPv4 /24 and /16 and IPv6 /48 and /32; classless
(< /24) delegation is not documented. So rDNS for the operator's /24 and
/48 is **not** on Cloudflare unless those `in-addr.arpa` / `ip6.arpa` zones
are delegated to Cloudflare in the RIR (RIPE `domain:` objects). If they
are, a small `cloudflare-rdns` module (`cloudflare_dns_record` type `PTR`,
name = reversed address, content = `ewr1.nodes.as215520.net`) can share the
same token. Anycast addresses should reverse to `git.as215520.net`; unicast
node addresses to their `nodes` names.

## 2. Design

### 2.1 Layout

```
infra/opentofu/
  modules/cloudflare-dns/       reusable module (this doc's subject)
  environments/dev/             root module: one POP (ewr1) + anycast
  environments/prod/            later: same module, full node map
```

Module inputs: `zone_name`, optional `zone_id`/`account_id`,
`service_hostname` (default `git`), `anycast_v4`, `anycast_v6`,
`nodes` (map of `{ipv4?, ipv6?}`, `for_each`), `nodes_subdomain`, `sshfp`,
`extra_records`, `ttl` (300), `node_ttl`, `manage_dnssec`, `comment`.
Outputs: `zone_id`, `service_fqdn`, `service_urls`, `node_fqdns`, per-group
record-ID maps, `dnssec_ds`.

Records produced for dev (`ewr1`): `git A`, `git AAAA`, `git SSHFP 4 2`,
`ewr1.nodes A`, `ewr1.nodes AAAA`; optional DNSSEC.

### 2.2 TTL: 300, not 60

Anycast addresses never change under failover - BGP withdraws the POP and
resolvers keep returning the same IP. A 60 s TTL on `git.<zone>` would only
raise query volume at Cloudflare and at client resolvers with zero
availability benefit. 300 s (Cloudflare's "Auto" value) is the default for
service and node records. The unicast node records are the only ones whose
values change (renumbering), so `node_ttl` can be dropped to 60 ahead of a
planned change and raised afterwards. Enterprise's 30 s minimum is irrelevant.

### 2.3 Proxying

`proxied = false` on every resource, with the reason in a header comment in
`main.tf`. There is no variable to turn it on; enabling it would require
editing the module, which is intended.

### 2.4 Secrets

`CLOUDFLARE_API_TOKEN` only ever exists in the environment of the `tofu`
process, injected by `sops exec-env`. Not a variable, not in tfvars, not in
state. `.gitignore` excludes `*.tfvars` except `*.tfvars.example`.

### 2.5 Migration path to a future brand domain

When a brand domain (call it `example.org`) is adopted:

1. Add its zone to Cloudflare (by hand or a rarely-applied `zones` root).
2. Instantiate the module a second time in the same environment:

   ```hcl
   module "dns_brand" {
     source           = "../../modules/cloudflare-dns"
     zone_name        = "example.org"
     service_hostname = "git"        # or "@" for gemini://example.org/
     anycast_v4       = var.anycast_v4
     anycast_v6       = var.anycast_v6
     sshfp            = var.sshfp    # same host key => same SSHFP
     nodes            = {}           # nodes stay under as215520.net
   }
   ```

   Both zones then publish identical A/AAAA/SSHFP; `as215520.net` keeps the
   infrastructure names (`nodes.`) permanently, the brand zone carries only
   the user-facing service name.
3. Why duplicated A/AAAA rather than `git.example.org CNAME git.as215520.net`:
   * A CNAME **cannot exist at a zone apex** (RFC 1034 s3.6.2: no other data
     may coexist with a CNAME, and the apex must carry SOA/NS). If the brand
     is `gemini://example.org/`, an apex CNAME is not an option. Cloudflare's
     "CNAME flattening" works around this only for *its* resolver output and
     is a Cloudflare-specific behaviour; duplicated addresses are portable.
   * A CNAME adds a second lookup and a cross-zone DNSSEC dependency.
   * SSHFP and other RRs cannot sit alongside a CNAME; the SSHFP for
     `git.example.org` would have to be found via the target, which OpenSSH
     handles by querying the canonical name only if `CanonicalizeHostname`
     is on. Duplicating the SSHFP at the brand name is simpler.
   * The addresses are two values that live in one variables file; the
     duplication cost is nil and OpenTofu keeps them in lock-step.
4. Later, when the brand is primary, `git.as215520.net` stays as the
   operator/infrastructure alias; nothing needs to be deleted.

### 2.6 Prod

`environments/prod` = same module with the full `nodes` map, `manage_dnssec
= true`, and a remote state backend with locking. Adding a POP is one line
in `nodes`; `for_each` keys are POP names so existing records are untouched.

## 3. Validation results (2026-09-10)

* Tofu: not present; installed **OpenTofu v1.12.6** linux_amd64 to
  `~/.local/bin/tofu` (SHA256SUMS verified).
* `tofu fmt -check -recursive infra/opentofu`: clean.
* `modules/cloudflare-dns`: `tofu init -backend=false` fetched
  `registry.opentofu.org/cloudflare/cloudflare 5.24.0`; `tofu validate`:
  "Success! The configuration is valid."
* `environments/dev`: same; valid.
* Extra check: a scratch root calling the module with a dummy token and a
  fixed `zone_id` (so no API call is made) ran `tofu plan` successfully:
  **9 to add** - `service_a`, `service_aaaa`, `service_sshfp["4-2"]`
  (data = {algorithm = 4, type = 2, fingerprint = ...}), `node["ewr1-a"]`,
  `node["ewr1-aaaa"]`, `node["fra1-aaaa"]`, `extra["caa"]` (data = {flags =
  0, tag = "issue", value = ";"}), `extra["txt"]`, `cloudflare_zone_dnssec`.
  This confirms the v5 nested `data` typing. One fix came out of it:
  `extra_records.data` had to be an explicit object type, not `any`, so map
  elements with and without `data` unify.

## 4. Sources

* Provider docs (raw markdown, main): `docs/resources/dns_record.md`,
  `docs/data-sources/zones.md`, `docs/data-sources/zone.md`,
  `docs/resources/zone_dnssec.md`, `docs/index.md`,
  `docs/guides/version-5-upgrade.md`; schema types from
  `internal/services/dns_record/schema.go`.
* Releases: github.com/cloudflare/terraform-provider-cloudflare (v5.24.0),
  github.com/opentofu/opentofu (v1.12.6); registry.opentofu.org versions API.
* Cloudflare docs: network ports, DNS record types, TTL reference, DNSSEC,
  multi-signer DNSSEC, reverse zones, CAA records, API token creation and
  templates.
* RFC 4255; IANA SSHFP parameters; OpenBSD `ssh-keygen(1)`, `ssh_config(5)`.
