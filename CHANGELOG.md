# Changelog

All notable changes are recorded here. Format: Added / Changed / Fixed /
Operations, newest first.

## Unreleased

## v0.1.0 - 2026-09-12

First release: the forge serves gemini://git.as215520.net/ anycast from
three points of presence and hosts its own source.

### Added
- Documentation site at `/docs/` (the user guide, `docs/site/`), served
  straight from the dogfooded repository; markdown tables render.
- Fleet status page at `/status/` (health, announcement state, replication
  lag per point of presence; components; incidents from
  `docs/site/incidents/` as a gemfeed) and the monitoring alerts feed at
  `/status/alerts`, read from the monitoring host's Prometheus over the mesh.
- Monitoring host `mon1` as code (Prometheus, blackbox, Grafana; BIRD
  exporter on every point of presence; `scripts/deploy monitor`).
- Git push forwarding: a push that reaches a replica is relayed to the
  repository's leader over the control plane.
- `forge admin release create|asset` for publishing releases from the
  command line; a certificate-expiry warning on the account and front pages.
- Storage and backup-age gauges are populated (`forge_disk_free_bytes`,
  `forge_repositories`, `forge_repository_bytes`, `forge_users`,
  `forge_backup_age_seconds`).
- Gemini read forge: repositories, trees, files, history, commits, diffs,
  refs, README rendering, gemfeeds and Atom.
- Identity by TLS client certificate with enrolment codes and revocation.
- Git over SSH with a restricted server; pre/post/proc-receive hooks.
- Issues, comments, releases with assets, repository settings over Titan
  and INPUT confirmations.
- Change review workflow: `refs/for/<branch>`, versions, interdiffs,
  patches, anchored reviews, server-side merges (ADR 0012).
- Replication between nodes with per-repository leadership and write
  forwarding; health-controlled anycast announcement.
- Backups (`forge admin backup`), restore, maintenance.
- Infrastructure as code: OpenTofu (Vultr, Cloudflare), cloud-init,
  systemd, nftables, BIRD and WireGuard generation, SOPS+age secrets, CI.

### Fixed
- `forge-backup` no longer writes into the read-only data directory (a
  success stamp made every run exit 1 after the archive was written).
- BIRD exporter unit flags match the Debian 1.4.2 build.
- `scripts/deploy` quotes remote script arguments (ssh flattens argv).

### Operations
- Rollouts follow `docs/runbooks/upgrade-and-rollback.md` per point of
  presence (pin, drain, withdraw, deploy, announce, unpin).
- First restore drill passed (`docs/disaster-recovery.md`, "Restore drills").
- The per-POP `/48` unicast addresses are on-net only; their blackbox
  probes were removed.
