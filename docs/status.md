# Project status

Updated: 2026-09-10

## Current milestone

**M0 Discovery / M1 Local read forge** in progress, in parallel.

## Accomplished

- Repository skeleton, license (Apache-2.0), contributing and security policy.
- ADRs 0001-0011 (language, single binary, SQLite, git CLI, identity, IaC, secrets, license, URL scheme, BIRD, single-writer).
- Go 1.27.1 toolchain installed under `~/.local/go` (no system Go, no sudo on this machine).

## Running services

None yet.

## Tests passing

None yet.

## Pending subagent work (M0)

| Agent | Topic | Output |
| --- | --- | --- |
| A | Gemini/Titan/Gemfeed/Misfin specs and clients | `docs/research/protocols.md` |
| B | Go vs Rust libraries | `docs/research/language-and-libraries.md` |
| C | AS215520 public state | `docs/network-readiness.md`, `infra/network/public-state.yaml` |
| D | Vultr BGP/BYOIP/IaC | `docs/research/vultr.md`, `infra/providers/vultr/facts.yaml` |
| E | Cloudflare DNS module | `docs/research/cloudflare-dns.md`, `infra/opentofu/modules/cloudflare-dns` |
| F | Threat model | `docs/threat-model.md` |

## Technical debt

- None recorded yet.

## Human blockers

None yet. Expected later: Vultr BGP/BYOIP ticket and LOA, RPKI ROA changes, IRR objects, provider credentials, spending approval.

## Next executable work

1. `forge serve` skeleton: config, Gemini listener, gemtext writer, router.
2. Git adapter: refs, tree, blob, log, commit, diff via git plumbing.
3. Store: SQLite migrations for users, certs, keys, repos.
4. Repository pages over Gemini; feeds.
5. Restricted SSH server and push/clone acceptance test (M2).
