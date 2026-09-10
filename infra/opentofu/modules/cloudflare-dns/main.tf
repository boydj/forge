# Cloudflare DNS for the forge: anycast service records, per-POP node records
# and SSHFP host-key fingerprints.
#
# Every record in this module is `proxied = false` ("DNS only" / grey cloud).
# Cloudflare's reverse proxy only carries HTTP(S) on a fixed port list
# (80/443/8080/8443/2052-2096 ...). Gemini (TCP/1965) and SSH (TCP/22) are
# neither HTTP nor on those ports, so a proxied (orange-cloud) record would
# publish Cloudflare edge IPs and silently break both services. Spectrum could
# proxy arbitrary TCP but is Enterprise-only and pointless for an anycast
# origin that already is its own edge. Proxying is therefore never enabled.

locals {
  # Zone lookup is optional: passing zone_id lets the token drop Zone:Zone:Read.
  zone_id = var.zone_id != null ? var.zone_id : data.cloudflare_zones.this[0].result[0].id

  service_fqdn = var.service_hostname == "@" ? var.zone_name : "${var.service_hostname}.${var.zone_name}"
  node_ttl     = coalesce(var.node_ttl, var.ttl)

  # Node records: one A and/or AAAA per POP, flattened into a single map so a
  # single for_each drives them and each record has a stable address.
  node_records = merge(
    {
      for name, n in var.nodes : "${name}-a" => {
        node    = name
        type    = "A"
        content = n.ipv4
      } if n.ipv4 != null
    },
    {
      for name, n in var.nodes : "${name}-aaaa" => {
        node    = name
        type    = "AAAA"
        content = n.ipv6
      } if n.ipv6 != null
    },
  )

  # Cloudflare stores SSHFP fingerprints upper-case (as ssh-keygen -r prints
  # them); normalise so lower-case input does not cause a perpetual diff.
  sshfp_records = {
    for r in var.sshfp : "${r.algorithm}-${r.type}" => {
      algorithm   = r.algorithm
      type        = r.type
      fingerprint = upper(r.fingerprint)
    }
  }

  # Accept relative names in extra_records; the API stores FQDNs, and using
  # FQDNs in config keeps plans clean.
  extra_records = {
    for k, r in var.extra_records : k => merge(r, {
      fqdn = (
        r.name == "@" ? var.zone_name :
        endswith(r.name, ".${var.zone_name}") ? r.name :
        "${r.name}.${var.zone_name}"
      )
    })
  }
}

# ---------------------------------------------------------------------------
# Zone lookup (read-only). The zone is deliberately NOT a resource here: it
# already exists (or is created once by hand / by a separate root module), and
# a data source cannot accidentally destroy it. If you ever want the zone under
# state control, `tofu import cloudflare_zone.this <zone_id>` in a separate
# module rather than adding a resource here.
# ---------------------------------------------------------------------------
data "cloudflare_zones" "this" {
  count = var.zone_id == null ? 1 : 0

  name = var.zone_name

  # `account` is a nested attribute in v5; only send it when narrowing.
  account = var.account_id == null ? null : { id = var.account_id }

  lifecycle {
    postcondition {
      condition     = length(self.result) == 1
      error_message = "Expected exactly one Cloudflare zone named ${var.zone_name}; got ${length(self.result)}. Check the token's zone scope or pass zone_id/account_id explicitly."
    }
  }
}

# ---------------------------------------------------------------------------
# Anycast service records: git.<zone> A/AAAA
# ---------------------------------------------------------------------------
resource "cloudflare_dns_record" "service_a" {
  for_each = toset(var.anycast_v4)

  zone_id = local.zone_id
  name    = local.service_fqdn
  type    = "A"
  content = each.value
  ttl     = var.ttl
  proxied = false # Gemini/SSH are not HTTP; see header comment.
  comment = "${var.comment}: anycast v4"
}

resource "cloudflare_dns_record" "service_aaaa" {
  for_each = toset(var.anycast_v6)

  zone_id = local.zone_id
  name    = local.service_fqdn
  type    = "AAAA"
  content = each.value
  ttl     = var.ttl
  proxied = false # Gemini/SSH are not HTTP; see header comment.
  comment = "${var.comment}: anycast v6"
}

# ---------------------------------------------------------------------------
# SSHFP (RFC 4255) for git.<zone>. Only meaningful to clients when the zone is
# DNSSEC-signed and the resolver validates (ssh's VerifyHostKeyDNS treats
# unsigned answers as "insecure" and falls back to asking).
# ---------------------------------------------------------------------------
resource "cloudflare_dns_record" "service_sshfp" {
  for_each = local.sshfp_records

  zone_id = local.zone_id
  name    = local.service_fqdn
  type    = "SSHFP"
  ttl     = var.ttl
  proxied = false # SSHFP is never proxiable; explicit for consistency.
  comment = "${var.comment}: sshfp alg=${each.value.algorithm} fptype=${each.value.type}"

  # v5: `data` is a nested attribute, not a block. algorithm/type are numbers.
  data = {
    algorithm   = each.value.algorithm
    type        = each.value.type
    fingerprint = each.value.fingerprint
  }
}

# ---------------------------------------------------------------------------
# Per-POP unicast records: <pop>.nodes.<zone> A/AAAA
# ---------------------------------------------------------------------------
resource "cloudflare_dns_record" "node" {
  for_each = local.node_records

  zone_id = local.zone_id
  name    = "${each.value.node}.${var.nodes_subdomain}.${var.zone_name}"
  type    = each.value.type
  content = each.value.content
  ttl     = local.node_ttl
  proxied = false # unicast management/diagnostic addresses; never proxied.
  comment = "${var.comment}: node ${each.value.node}"
}

# ---------------------------------------------------------------------------
# Free-form extra records (TXT for verification/SPF, CAA, MX, ...)
# ---------------------------------------------------------------------------
resource "cloudflare_dns_record" "extra" {
  for_each = local.extra_records

  zone_id  = local.zone_id
  name     = each.value.fqdn
  type     = upper(each.value.type)
  content  = each.value.content
  data     = each.value.data
  priority = each.value.priority
  ttl      = coalesce(each.value.ttl, var.ttl)
  proxied  = false # nothing in this zone is HTTP behind Cloudflare.
  comment  = coalesce(each.value.comment, var.comment)
}

# ---------------------------------------------------------------------------
# DNSSEC (optional). Enabling produces a DS record that must be installed at
# the registrar; until it is, SSHFP records are not "secure" to ssh clients.
# ---------------------------------------------------------------------------
resource "cloudflare_zone_dnssec" "this" {
  count = var.manage_dnssec ? 1 : 0

  zone_id = local.zone_id
  status  = "active"

  # Single-provider zone: leave multi-signer and pre-signed off.
  dnssec_multi_signer = false
  dnssec_presigned    = false
  dnssec_use_nsec3    = false
}
