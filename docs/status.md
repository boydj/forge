# Project status

Updated: 2026-09-10 (evening)

## Current milestone

**M10-style hardening done for the software (security review remediated,
failure injection, load smoke, real-client interoperability); M11 Mercurial
prototype done; M4 single-VPS deployment and M6-M9 network milestones wait
on operator actions (credentials, Vultr BGP approval, ROA, the /48
decision).**

## Accomplished

- Repository skeleton, license (Apache-2.0), contributing and security policy.
- ADRs 0001-0011 (language, single binary, SQLite, git CLI, identity, IaC, secrets, license, URL scheme, BIRD, single-writer).
- Go 1.27.1 toolchain installed under `~/.local/go`; tofu, sops, age, vultr-cli under `~/.local/bin` (no sudo on this machine).
- M0 research: `docs/research/{protocols,language-and-libraries,vultr,cloudflare-dns}.md`, `docs/threat-model.md`, `docs/network-readiness.md`.
- M1: Gemini server, gemtext builder, git adapter, SQLite store, forge services, pages (front, user, repo overview/README, tree, blob, raw, log, commit+diff, refs), gemfeeds + Atom, account registration by client certificate, enrolment codes, SSH key management (INPUT + Titan), admin CLI.
- M2: restricted SSH server (`internal/sshd`), hook socket protocol, pre-receive ACL/quota/ref policy, post-receive events. **First acceptance test passes** (`tests/acceptance_test.go`): create repo -> git clone -> git push -> Gemini browse, plus permission, private-repo and hook-denial checks.
- M3: issues and comments, releases with assets, repository settings, and the
  change/review workflow of ADR 0012 (push to `refs/for/<branch>`, versions,
  interdiffs, patches, anchored reviews, server-side merge with
  `merge-tree`). **Second acceptance test passes** (register -> Titan issue ->
  Gemini read -> Titan comment -> gemfeed) and **third acceptance test passes**
  (propose -> review -> merge).
- M5 (code): `internal/repl` control plane over the WireGuard/loopback
  network (shared secret), per-origin event cursors, metadata snapshots, git
  over stateless smart HTTP, replica status, resync, leader move, Titan write
  forwarding; `scripts/dev-cluster` for local multi-node runs.
- Backup/restore: `forge admin backup --out x.tar.gz` (DB snapshot + git
  bundles + assets + identities), `forge admin restore`, `forge admin
  maintenance [--check]` (gc, fsck, size refresh, PRAGMA optimize).
- Docs: development, titan, gemfeeds, vcs-interface, operations, tls,
  disaster-recovery, dogfooding, misfin, replication, review-workflow,
  git-ssh, self-hosting, secrets, costs, health.
- Hardening: `docs/security-review.md` (25 findings, 21 remediated on main,
  action tokens for every INPUT-driven write), 14 fuzz targets, failure
  injection suite (`tests/failure_test.go`, 11 scenarios), load smoke test and
  `tools/loadtest`, interoperability with gg/titan/gemget/ignition/feedparser
  (`scripts/interop`, 78 checks), monitoring as code (`infra/monitoring`),
  15 runbooks, release tooling, dependency review.
- M11: `internal/vcs/hg` Mercurial adapter prototype (read paths, patches;
  merges unsupported), `docs/mercurial.md`.
- M6 prep: IRR/RPKI/PeeringDB desired state, Vultr LOA and BGP checklist,
  `scripts/netcheck` drift checker, `docs/runbooks/network-bootstrap.md`.
- Network: `infra/network/address-plan.yaml`, `scripts/netgen` (BIRD, WireGuard, DNS, nftables outputs), `scripts/bgp-announce`, BIRD configs validated with `bird -p` 2.14.
- DNS: `infra/opentofu/modules/cloudflare-dns` validated with tofu 1.12.6.

## Running services

None deployed. Local: `make run` serves gemini://localhost:1965/ and ssh://localhost:22 (use `forge admin init --ssh-listen :2222` for unprivileged ports).

## Tests passing

- `go test ./...`: gemini, config, vcs/git, store, forge, web, sshd, tlsid.
- `go test -tags integration ./tests/`: three acceptance tests (M2 clone/push/browse, M3 Titan issue flow, ADR 0012 change workflow).
- `go test ./internal/repl`: two-node replication scenarios (sync, interrupted fetch, leader move, auth).
- `python3 -m unittest discover -s tests/network`: 21 network generator tests.

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

- Security review open items: SR-02 decrypted secrets on persistent disk
  (tmpfs delivery in progress), SR-19a/b deploy sudo scope and first-deploy
  TOFU (in progress), SR-24 informational.
- Forwarded Titan writes trust the replica's TLS verification of the client
  certificate (documented in `docs/replication.md`).

- Titan writes use certificate-only authorisation; the threat model proposed
  an additional single-use token. Deviation recorded in `docs/titan.md`
  (Titan uploads are explicit client actions, not link clicks; clients drop
  token parameters from links anyway). Revisit if abuse appears.
- Markdown-to-gemtext converter covers a practical subset only.
- `receive-pack` uses git protocol v0 (git has no v2 push); fine.
- No dual host-key rotation overlap yet (threat model T-36).

## Human blockers

None blocking application work. Items that will need the operator (batched, not yet urgent):

1. Vultr: BGP request form + LOA for 44.32.58.0/24 and 2a0f:85c1:368::/48; account instance-limit increase; API key.
2. RPKI: ROA for 44.32.58.0/24 (via ARDC, ARIN hosted RPKI); route object exists in RADB (MAINT-ARDC).
3. Decision: the /48 is live via AS835 (Toronto). Choose migrate-to-forge-POPs vs a separate /48 (see `docs/network-architecture.md` section 6).
4. Reverse DNS delegation for both prefixes (Inferno `domain` object; ARDC portal).
5. Cloudflare API token (Zone:DNS:Edit, Zone:Zone:Read) for `as215520.net`; publish DS record at Namecheap.
6. Spending approval for the first Vultr instance (~$5-6/month).

## Next executable work

1. Finish tmpfs secrets delivery and scoped sudo (infra agent).
2. M4: `tofu plan/apply` for the dev environment once credentials and spend approval exist; then `scripts/deploy ewr1` and the runbook smoke test.
3. M6/M7: operator actions in `docs/runbooks/network-bootstrap.md`; `scripts/netcheck --expect announced` after Vultr approval.
4. M8/M9: second POP, then a second provider module (`infra/opentofu/modules/<provider>-pop`).
5. Dogfooding once M4 is live and backups verified (`docs/dogfooding.md`).
6. Optional: push the repository to the configured `origin` (github.com/boydj/forge) on operator request.
