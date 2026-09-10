# 0001. Implementation language: Go

Date: 2026-09-10
Status: accepted

## Context

The forge needs a Gemini/Titan TLS server with client certificates, an SSH server
that only runs `git-upload-pack`/`git-receive-pack`, a Git adapter, SQLite
metadata, Prometheus metrics and an admin CLI, shipped as a static binary to small
VPSes. Candidates were Go and Rust (see `docs/research/language-and-libraries.md`).

## Decision

Go.

- `crypto/tls` accepts self-signed client certificates with `RequestClientCert`
  plus a custom verifier; no CA machinery is needed for TOFU identities.
- `golang.org/x/crypto/ssh` is the production SSH server used by Gitea, Gogs and
  soft-serve; it gives full control over the exec channel with no shell.
- Git transport is `git-upload-pack`/`git-receive-pack` executed as subprocesses
  in both languages, so language choice does not affect Git correctness.
- `modernc.org/sqlite` gives cgo-free SQLite and a fully static binary.
- Goroutines fit thousands of idle TLS and SSH connections without ceremony.
- Development velocity matters more than peak performance for a forge whose
  hot paths are Git subprocesses and disk.

Rust remains a fine choice; rustls client-auth customisation and russh
maturity were the deciding friction points.

## Consequences

- One Go module, toolchain pinned in `go.mod`, static builds with `CGO_ENABLED=0`.
- `govulncheck` and `go.sum` are part of CI.
- Memory safety of the process is Go's; the Git subprocess boundary remains the
  main isolation line and is hardened separately (see threat model).
