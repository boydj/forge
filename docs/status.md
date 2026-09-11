# Project status

Updated: 2026-09-10 (evening)

## Current milestone

**M8 complete: two POPs, ewr1 (Vultr New Jersey) and ams1 (Vultr
Amsterdam), announce 44.32.58.0/24 and 2a0f:85c1:368::/48 via Vultr
AS20473 as anycast; they are joined by WireGuard and replicate (metadata
leader ewr1). git.as215520.net resolves to 44.32.58.1 and
2a0f:85c1:368:1::1. The first account (jdb, administrator) exists.** M9
(second provider) is next. Hardening (security review, failure
injection, load, interop), M11 Mercurial prototype and M6 desired state are
done.

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

- **ewr1** (Vultr `ewr`, 64.176.195.46 / 2001:19f0:4000:3e3a:5400:06ff:feac:7bb3) and
  **ams1** (Vultr `ams`, 78.141.214.215 / 2a05:f480:1400:3d99:5400:06ff:feac:9af4):
  forge (Gemini/Titan :1965, Git SSH :22), forge-secrets (tmpfs), nftables,
  wg0 mesh (handshakes both ways), replication over the mesh (metadata
  leader ewr1), BIRD with `vultr4`/`vultr6` Established and both prefixes
  exported from both nodes. DNS: `<pop>.nodes.as215520.net`,
  `git.as215520.net` A/AAAA on the anycast addresses, SSHFP.
- Local: `make run` serves gemini://localhost:1965/ and ssh://localhost:2222.
- **Dogfooding**: this repository is hosted on the forge as `jdb/forge`
  (leader ewr1, replicated to ams1); browse gemini://git.as215520.net/~jdb/forge/.

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

- Security review: all findings remediated except SR-24 (informational).
  Existing nodes (none deployed yet) would migrate with `deploy sysupdate`.
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

Resolved 2026-09-11: /48 migrates to the forge POPs (ADR 0013); cost
approved; Vultr and Cloudflare tokens in `infra/secrets/dev.enc.yaml`; Vultr
BGP enabled for AS215520 with both prefixes registered; no ROA possible for
the /24 (expected not-found).

Remaining operator actions:

1. Done 2026-09-11: BGP go-live (M7). The website origin
   `as215520.net`/`www` AAAA `2a0f:85c1:368::b00b` lived on the Toronto box
   and is unreachable now; move it (ewr1 can carry any /48 address) or
   re-point the records.
2. Done 2026-09-11: `as-set AS215520:AS-ALL` created and `aut-num AS215520`
   updated in the RIPE Database from `infra/opentofu/environments/ripe`
   (frederic-arr/ripedb provider; plan is clean). After the Toronto
   withdrawal, drop the AS835 lines from `infra/network/irr/aut-num-AS215520.rpsl`
   and apply again.
3. Reverse DNS delegation for both prefixes (Inferno `domain` object; ARDC portal NS records).
4. Publish the DS record at Namecheap; fill PeeringDB (`infra/network/peeringdb/desired.yaml`).
5. Register the first account (it becomes administrator): open
   gemini://git.as215520.net/account with a client certificate in Lagrange.

## Next executable work

1. M9: second provider module (`infra/opentofu/modules/<provider>-pop`) with the same outputs as vultr-pop; candidates in `docs/research/vultr.md` section 11.
2. IPv4 /24 anycast reachability is uneven from some networks (no ROA possible, ADR 0013; observed a Cogent/Marseille detour). The IPv6 /48 anycast is clean. Monitor and consider a covering-route or upstream fix.
3. Git push over anycast can hit a replica (rejected with the leader name); automatic push forwarding is future work.
2. M6: RIPE objects, rDNS, DS, PeeringDB (operator).
3. Backup verification on ewr1 (`forge-backup.timer` runs nightly; check `/var/backups/forge`).
4. M8/M9: second POP, then a second provider module (`infra/opentofu/modules/<provider>-pop`).
5. Dogfooding once M4 is live and backups verified (`docs/dogfooding.md`).
6. Pushed to `origin` (github.com/boydj/forge) on 2026-09-10; GitHub Actions runs `.github/workflows/ci.yml` on every push.
