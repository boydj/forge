# 0013. Migrate the existing /48 to the forge POPs; IPv4 /24 without ROA

Date: 2026-09-11
Status: accepted

## Context

`2a0f:85c1:368::/48` (RIPE, ROA valid for AS215520) is announced today from
Toronto via AS835. Anycast from the forge POPs requires announcing the whole
/48, so the two uses cannot coexist without Toronto becoming a POP that does
not run the forge (`docs/network-architecture.md` section 6).

`44.32.58.0/24` is ARDC 44Net space under ARIN. ARDC cannot create a ROA and
ARIN provides neither RPKI nor IRR services for legacy resources outside an
ARIN service agreement, so the /24 cannot get a ROA.

## Decision

- The /48 is migrated to the forge POPs (option b). Once the first POP
  announces it and `scripts/netcheck --expect announced` is clean, the AS835
  announcement is withdrawn by the operator. The lab /64 and per-POP /64s in
  `infra/network/address-plan.yaml` stand.
- The /24 is announced RPKI **not-found** ("unknown"). Vultr already lists it
  for the account with `rpki_status: UNKNOWN`, which is accepted. The
  RADB route object maintained by ARDC (`MAINT-ARDC`) is the IRR evidence.
  `infra/network/rpki/desired-roas.yaml` and `scripts/netcheck` expect
  `not-found` for the /24 rather than `valid`.

## Consequences

- IPv4 reachability depends on networks that accept not-found routes (the
  overwhelming majority; only ROV "invalid" is dropped). Should a ROA ever
  become possible (ARDC obtaining ARIN RPKI coverage, or a different /24),
  switch the expectation back to `valid`.
- During migration the /48 is announced from two origins' paths (AS835 and
  AS20473 -> AS215520) for a short overlap; both are RPKI-valid for
  AS215520, so no invalid state occurs.
