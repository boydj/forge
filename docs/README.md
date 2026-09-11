# Forge documentation

A source forge on Gemini, Titan and Git over SSH, served anycast from
AS215520. This directory is the documentation site: it is published at
`/docs/` straight from the repository, so a git push is a docs deploy.
Everything else about the project, including the infrastructure, is code in
the same repository.

## Using the forge

- [Project status](status.md): what works today, what is next.
- [Protocol architecture](protocol-architecture.md): how Gemini, Titan and SSH fit together and which does what.
- [Titan](titan.md): writing to the forge (issues, comments, uploads) with a client certificate.
- [Git over SSH](git-ssh.md): cloning and pushing, host keys, SSHFP.
- [Proposing, reviewing and merging changes](review-workflow.md): the `refs/for/<branch>` change model.
- [Gemfeeds](gemfeeds.md): subscribing to repositories, users and the whole site.
- [TLS and host-key identity](tls.md): certificate identity, trust on first use, what to pin.
- [Interoperability](interop.md): which third-party clients are tested.

## How it is built

- [Architecture](architecture.md): the single binary, its packages and data flow.
- [VCS interface](vcs-interface.md): the version-control abstraction and the git backend.
- [Mercurial prototype](mercurial.md): the second backend.
- [Replication](replication.md): single-writer leadership, control plane, replicas.
- [Health worker and announcement control](health.md): how a POP decides whether to attract anycast traffic.
- [Network architecture](network-architecture.md): anycast, BGP, WireGuard mesh, the address plan.
- [Threat model](threat-model.md) and the [security review](security-review.md).
- [Decisions](decisions/README.md): the architecture decision records.
- [Misfin](misfin.md): notifications, deferred.

## Running it

- [Self-hosting a forge node](self-hosting.md): one node, no anycast.
- [Operations](operations.md): configuration reference, directory layout, admin CLI, units, logs, metrics.
- [Runbooks](runbooks/README.md): copy-pasteable procedures for the fleet.
- [Secrets](secrets.md): the SOPS and age setup.
- [Monitoring](monitoring.md): Prometheus, alert rules, dashboards.
- [Disaster recovery](disaster-recovery.md) and [failure testing](failure-testing.md).
- [Release procedure](release-procedure.md), [dependency review](dependencies.md), [running costs](costs.md).
- [Network readiness](network-readiness.md): the public registry state of AS215520.

## Contributing

- [Development](development.md): toolchain, tests, layout.
- [Dogfooding](dogfooding.md): the forge hosts its own source.
