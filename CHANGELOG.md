# Changelog

All notable changes are recorded here. Format: Added / Changed / Fixed /
Operations, newest first.

## Unreleased

### Added
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
