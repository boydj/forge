# 0007. Secrets: SOPS + age

Date: 2026-09-10
Status: accepted

## Context

Everything as code, but no plaintext secrets in Git. Candidates: SOPS+age,
external secret manager, ad hoc encrypted files.

## Decision

SOPS with age recipients. `.sops.yaml` maps paths under `infra/secrets/` to
recipient sets (operator keys, per-node keys). Encrypted files are committed;
decryption happens on the operator machine for IaC and on nodes at deploy time
with the node's age key.

## Consequences

- One `.sops.yaml`, one `infra/secrets/` tree, one `scripts/secrets` helper.
- Rotation is a re-encrypt with `sops updatekeys` plus a deploy.
- No external service dependency for secrets.
