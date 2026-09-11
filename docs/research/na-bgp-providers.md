# North America BGP VPS providers — code-drivability evaluation

**Question:** Should we add a second BGP provider alongside Vultr for the AS215520 anycast
forge? The operator's constraint: *only* add one if it is **fully code-driven** — a public
compute API (or Terraform/OpenTofu provider) that can create and destroy instances, and BGP
sessions that can be turned up without a human ticket — mirroring how Vultr is driven from
`infra/opentofu` + `scripts/bgp-announce`. North America only.

**Source list:** https://bgp.services/bgp-vps-providers (filtered to US/Canada-located providers).

**Verdict: do not add one. No North America BGP VPS provider is fully code-driven.**
Vultr (AS20473) remains the only provider that combines API/Terraform-driven compute with
API-driven BGP. 29 NA-located providers were evaluated; none clears the bar.

## Rating definitions

- **FULLY-CODE-DRIVEN** — documented public compute API or TF provider that creates *and*
  destroys instances, AND BGP sessions obtainable without a human ticket.
- **PARTIAL** — real public compute API exists, but BGP is a manual/ticket step (or the API
  key itself requires a human bootstrap).
- **PANEL-ONLY** — no usable public customer compute API; provisioning is web-panel/WHMCS only.
- **N/A** — not actually a North-America-located, bookable BGP VPS.

## Summary

| Verdict | Count | Providers |
|---|---|---|
| FULLY-CODE-DRIVEN | 0 | — |
| PARTIAL | 4 | Xenyth Cloud, ARP Networks, RackCorp, HYEHOST |
| PANEL-ONLY | 24 | everything else |
| N/A | 1 | PP-Networks (Europe-only VPS) |

## The four PARTIAL providers (closest to the bar)

| Provider | ASN | NA location | Compute API | BGP | Why not fully code-driven |
|---|---|---|---|---|---|
| **Xenyth Cloud** | AS62513 | Toronto, ON | Yes — documented REST create/destroy (`ostkkvm`), Bearer auth | **Self-service, no ticket** (dashboard ASN verify, peer details in-panel) | API key issued via a one-time support ticket; BGP is panel-driven not API-driven, and lives on the Edge/PVE line, not the API-managed VM line |
| **ARP Networks** | AS25795 | Los Angeles | Yes — `POST`/`DELETE /api/v1/servers`, Bearer, no key ticket | Manual panel/"contact us" $10/mo add-on | BGP is not documented or API-driven |
| **RackCorp** | AS56038 | LA + N. Virginia | Yes — REST v2.8 `POST /order/create/server` (create-focused), UUID+secret | Support ticket; peer IPs/communities documented but not API | Quote-only pricing; BGP ticketed |
| **HYEHOST** | AS47272 | Ashburn, VA | VirtFusion underneath, no documented customer token | Self-service panel, no ticket claimed | Compute API undocumented for customers; reliability concerns (public complaint thread) |

**Best of the four: Xenyth Cloud** — the only one pairing a documented create+destroy compute
API with ticket-free BGP. If provider diversity ever becomes a hard requirement, Xenyth
(Toronto) is the candidate to revisit, accepting a one-time human step to mint the API key and
a panel action (not an API call) to order the BGP session. ARP Networks is the runner-up on
clean compute automation but its BGP is manual.

## Why nothing qualifies

The small-operator BGP VPS market is built on panels, not APIs:

- **The panel APIs are admin-scoped.** VirtFusion, SolusVM, and Virtualizor all ship APIs, but
  the create/destroy operations are reseller/admin endpoints behind a provider-generated token.
  None of the VirtFusion shops (F4, HYEHOST, Elcro, iWebFusion, SHIFT, RackGenius, Valor Node)
  hands customers a token that can create or destroy their own VM. Xenyth is the exception —
  a bespoke stack with a real customer create/destroy API.
- **BGP is a human turn-up almost everywhere.** Even providers with a good self-service
  reputation (BuyVM, Terabit, Tritan) gate the ASN/session behind a ticket or LOA. Only Xenyth,
  Neptune, and HYEHOST offer no-ticket self-service BGP, and none of those exposes it via API.
- **No Terraform/OpenTofu provider exists for any of them.** The only VirtFusion community
  provider (EZSCALE) is admin-scoped, WIP, and last released v0.0.3 in 2023.

## Full results by batch

Per-provider detail with cited URLs is in the batch files:

- Batch A — [na-bgp-providers-A.md](na-bgp-providers-A.md): RackCorp, BuyVM/FranTech, HostUS,
  ARP Networks, Terabit Systems, Xenyth Cloud, Valor Node.
- Batch B — [na-bgp-providers-B.md](na-bgp-providers-B.md): Sucura Networks, Tier.Net, Xentain
  Solutions, F4 Networks, Skywolf Cloud, SolidNetDC, IonSwitch. (All PANEL-ONLY.)
- Batch C — [na-bgp-providers-C.md](na-bgp-providers-C.md): EasyVM, Elcro Digital, HYEHOST,
  iWebFusion, Mean Servers, Neptune Networks, SHIFT HOSTING.
- Batch D — [na-bgp-providers-D.md](na-bgp-providers-D.md): Panix, Colorado Colo, Tritan
  Internet, RackGenius, Rackoona, Sunbreak Electronics, Ohz, PP-Networks.

## Recommendation

Keep Vultr as the sole BGP transit provider for now; it is the only option that fits the
everything-as-code model. Provider diversity via a second BGP upstream is **not achievable
fully in code today** in North America. If we later accept a one-time manual bootstrap per POP
(mint an API key, click to order the BGP session), **Xenyth Cloud in Toronto** is the
recommended candidate — and the existing `add-virtua-pop.md` runbook pattern (API-driven VM +
manual BGP session step) already models exactly that trade-off.
