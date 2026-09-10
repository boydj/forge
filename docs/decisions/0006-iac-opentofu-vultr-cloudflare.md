# 0006. Infrastructure as code: OpenTofu, Vultr first, Cloudflare DNS

Date: 2026-09-10
Status: accepted

## Context

The operator prefers Cloudflare for DNS and Vultr for VPS, with a requirement
that a second BGP-capable provider be addable later.

## Decision

- OpenTofu with modules `cloudflare-dns`, `vultr-pop`, `forge-node`
  (provider-agnostic node configuration). Environments `dev`, `staging`,
  `production` compose modules.
- Cloudflare is DNS authority only; proxying is never enabled for forge names.
- Host configuration is applied by a small, idempotent, source-controlled
  provisioning path (cloud-init bootstrap plus `scripts/deploy`), not by
  hand.
- Provider-specific facts (BGP peer addresses, communities, plan IDs) live in
  `infra/providers/<name>/facts.yaml`.

## Consequences

- Adding a provider means a new `modules/<provider>-pop` producing the same
  outputs consumed by `forge-node`.
- State backends are per environment; production state is encrypted at rest
  and never committed.
