# 0002. One binary, several subcommands

Date: 2026-09-10
Status: accepted

## Context

The system needs a Gemini/Titan listener, an SSH listener, replication workers,
an admin CLI and an SSH-side command handler.

## Decision

A single binary `forge`:

- `forge serve` runs Gemini, Titan, SSH, replication and metrics in one process
  supervised by systemd.
- `forge admin ...` is the operator CLI. It talks to the same SQLite database
  and repository store on the local node, and to peers over the control network
  where needed.
- `forge hook <name>` is invoked by Git server-side hooks (`pre-receive`,
  `update`, `post-receive`) and communicates with the daemon over a Unix socket.

## Consequences

- One artefact to build, sign, deploy and roll back.
- A crash of one listener takes the process down; systemd restarts it. This is
  acceptable for v1 and simpler than supervising several daemons.
- Configuration is one file (`/etc/forge/forge.toml`) plus environment overrides.
