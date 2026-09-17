# 0014. Platform and services: repository boundaries and the service contract

Date: 2026-09-16
Status: accepted

## Context

AS215520 currently runs one service, the forge, on three anycast POPs plus a
monitoring host. The operator intends to run more small-internet services
(gopher, finger, Misfin, NTP and similar) on the same network.

Two things live in this repository today and are not the same kind of thing:

- **The forge**: a Go application other people can run. `docs/self-hosting.md`
  documents doing exactly that, with no AS215520 involvement.
- **The AS215520 platform**: one operator's network. The address plan, netgen
  (BIRD, WireGuard, nftables, DNS), the POP OpenTofu modules, cloud-init,
  `scripts/{deploy,secrets,bgp-announce,netcheck}`, the monitoring stack and
  the secrets bundle.

The addressing already anticipated more services: the plan reserves
`2a0f:85c1:368:2::/64` .. `:ff::/64` and `44.32.58.2-15` for future anycast
services, and POPs carry `roles:` rather than assuming the forge. The node
layer did not: cloud-init named the forge 123 times and nftables hardcoded
`{22, 1965}`.

The decisive constraint is that **anycast is per-POP, not per-service**. BGP
announces a prefix from a node; it cannot announce "the gopher service".
Once a POP runs several services, one health verdict decides whether that
node attracts traffic for all of them.

## Decision

**Boundary.** Portable software and this operator's network live in
different repositories:

| Repository | Contents |
| --- | --- |
| platform | address plan, netgen, OpenTofu modules, base cloud-init, deploy/secrets/bgp-announce/netcheck, monitoring, network ADRs and runbooks. Owns the service contract |
| forge | the application, its own docs, releases and mirror |
| services | the small daemons, one Go module, one release stream |

A 200-line finger daemon does not get its own repository, CI, release
process and runbook set; the forge, at 20k lines with its own users and
self-hosting story, does not become a subdirectory of an infrastructure
monorepo.

**Service contract.** A service declares: a systemd unit, health checks with
their criticality, the TCP ports it needs public and mesh-only, whether it
binds anycast addresses, and a metrics endpoint. The platform provisions
POPs from that declaration; it does not know what any service does.

**Criticality.** Each health check declares whether its failure justifies
withdrawing the POP:

- critical failures drive the existing state machine (drain, then withdraw);
- non-critical failures leave the node announced and are reported as
  degraded on `/status`, the fleet page and in metrics.

The zero value is critical: an unclassified check withdraws the POP rather
than failing silently. Replication lag is reclassified non-critical — a
lagging replica still serves reads, and withdrawing on lag is the mechanism
by which one slow leader becomes a fleet-wide outage.

**Staging.** The split happens when the second service exists, not before;
churning a live system buys nothing today. The preparation is done now so
the extraction is mechanical: the reusable packages are promoted out of
`internal/`, firewall ports come from the service catalogue, the address
plan lists services, and criticality is implemented.

## Consequences

- `pkg/{gemini,tlsid,health,metrics,config}` are importable by other
  modules. The health controller — the hysteresis state machine that makes
  anycast safe — is shared rather than reimplemented per service, which is
  the single most valuable thing to not get wrong twice.
- Promoting those packages out of `internal/` makes them public API of this
  module: their signatures now need care across the split.
- A failing non-critical check no longer withdraws a POP. This is a
  behaviour change: a badly lagging replica now stays in rotation and
  alerts instead.
- POPs are 1 GB with the forge capped at 600 MB. More services per POP means
  larger instances; that is a cost decision per service, not a platform one.
- Until the split, this repository contains both layers. The boundary is
  directory-level and enforced only by convention.
