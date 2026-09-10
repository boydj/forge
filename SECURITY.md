# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately rather than in a public issue.

Until the forge hosts itself, report by email to the address listed in the
`abuse-mailbox` of AS215520 (see `docs/network-readiness.md`) with the subject
`forge security`. Once the forge is live, a private issue tracker path will be
published here.

Include: affected component, reproduction steps, impact, and whether the issue is
already public. We will acknowledge within 5 business days.

## Scope

- The `forge` binary: Gemini, Titan, SSH/Git transport, admin CLI, replication.
- Deployment code under `infra/` (BIRD, nftables, WireGuard, systemd, OpenTofu).

## Design references

- Threat model: `docs/threat-model.md`
- Security invariants: `docs/threat-model.md#security-invariants`

## Supported versions

Pre-1.0: only the `main` branch receives fixes.
