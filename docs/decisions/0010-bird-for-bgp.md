# 0010. BIRD 2 for BGP

Date: 2026-09-10
Status: accepted

## Decision

BIRD 2.x on each POP, configuration generated from `infra/network/topology.yaml`
and `infra/network/address-plan.yaml`. Health-controlled announcements are
implemented by a `forge` health check toggling a BIRD filter constant via
`birdc` with hysteresis (see `docs/network-architecture.md`).

Rationale: BIRD is the reference implementation in Vultr's BGP documentation,
is lightweight, has simple text configuration suited to templating, and
supports RPKI (RTR), graceful shutdown and communities natively.

## Consequences

- FRR is not used; if a provider requires it a second template can be added.
- Templates and a config-check (`bird -p`) run in CI.
