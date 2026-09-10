# 0011. Single writer per repository; replicas pull

Date: 2026-09-10
Status: accepted

## Context

Anycast sends a client to the nearest POP; Git pushes and Titan writes must
not race across POPs.

## Decision

- Every repository has exactly one **leader** node recorded in metadata.
- Reads (Gemini, feeds, clone, fetch) are served by any node from its local
  replica.
- Writes (push, Titan) arriving at a non-leader node are forwarded to the
  leader over the WireGuard control network; the client sees one request.
- After a write, the leader records an event; replicas fetch (`git fetch` over
  the control network) and apply metadata events in order.
- Leadership moves with `forge admin repo move-leader`, which drains, syncs and
  swaps atomically; automatic failover is not in v1.

No consensus protocol. A lost leader means writes for its repositories fail
until leadership is moved; reads continue everywhere.

## Consequences

- Simple, inspectable, no multi-master conflicts.
- Write latency includes one control-network hop for remote POPs.
