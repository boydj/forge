# North America BGP VPS Providers — Batch B

Research goal: find a **fully code-driven** second BGP provider alongside Vultr (North America only).
"Fully code-driven" = a documented **public compute API** (or Terraform/OpenTofu provider) that can create AND
destroy a VPS, **AND** a customer BGP session / BYOIP obtainable without a human support ticket.

Skeptical rule applied: an API only counts if **real customer-facing docs exist**. Panels like VirtFusion / SolusVM /
Virtualizor all ship APIs, but those are **admin/reseller** APIs behind a token generated in the provider's admin area —
they are **not** exposed to end customers unless the provider explicitly documents customer API access. None of the
providers below do.

_Researched 2026-09-11. Web-search budget was exhausted mid-research; a few "likely/unconfirmed" cells are noted._

| Provider | NA locations | Panel | Compute API (URL) | TF provider | BGP provisioning | ASN | Price | Verdict |
|---|---|---|---|---|---|---|---|---|
| **Sucura Networks** | Toronto, Montreal, Chicago, NYC, Ashburn, Dallas | Custom **Nexus Panel** + WHMCS billing | No public/documented API found | None | "Contact us to set up BGP" (marketed as self-service in Nexus, but VM page says contact) — not programmatic | AS398999 | $3.75/mo (Toronto) / $7.50/mo (US) | **PANEL-ONLY** |
| **Tier.Net** | Ashburn, Bend, Charlotte, Dallas, NYC (+ Binghamton) | **SolusVM** + WHMCS | No customer API docs (SolusVM API is admin-side) | None | BGP communities documented; session setup via ticket | AS397423 | from $7.49/mo | **PANEL-ONLY** |
| **Xentain Solutions** | Dallas, Fremont (US) + Vancouver, CA | "VPS Panel" (vps.xentain.com), software unconfirmed | No public API found | None | Free IPv6 BGP transit via tunnel; network services listed "Currently Unavailable"; ticket-based | AS15353 | not published | **PANEL-ONLY** |
| **F4 Networks** | Kansas City, San Francisco, NYC | **VirtFusion** (store.f4.network); reported, not 100% confirmed | VirtFusion API is admin/reseller-only; no customer API docs | None | Free BGP/BYOIP included, but provisioned manually (order/ticket) | AS21738 | ~$6/mo (4GB) / $24/yr (1GB), BGP free | **PANEL-ONLY** |
| **Skywolf Cloud** | Fremont (+ Hong Kong) | Unconfirmed (VirtFusion-class) | No public API found | None | BGP communities/passthrough; "BGP not for commercial use"; ticket-based | AS7720 | from $2.50/mo | **PANEL-ONLY** |
| **SolidNetDC / SolidVPS** | Los Angeles | Unconfirmed (kb.solidvps.com) | No public API found | None | Not documented; ticket-based | AS14401 | from $2.95/mo | **PANEL-ONLY** |
| **IonSwitch** | Coeur d'Alene, Idaho (portal: vps.ionswitch.com) | **Virtualizor** | No public customer API docs (Virtualizor enduser API not exposed/documented by IonSwitch) | None | "Free IP announcements and BGP sessions" — provisioning method undocumented, ticket-based | AS16584 | from $3.50/mo | **PANEL-ONLY** |

## Per-provider notes
- **Sucura Networks** — Real NA footprint and instant auto-provisioning VMs via proprietary Nexus panel; the most automation-forward of the batch, but there is **no documented public API and no Terraform provider**, so compute cannot be driven by code. BGP is set up through the panel/support, not an API.
- **Tier.Net** — Managed VPS on SolusVM with WHMCS billing. SolusVM does have an API, but it is administrator/reseller-facing; Tier.Net publishes no customer compute API. BGP communities are documented (billing.tier.net/knowledgebase/37) but sessions are turned up by staff.
- **Xentain Solutions** — Tiny network (AS15353, ~1 peer). US presence at Dallas + Fremont plus Vancouver HQ, so it *is* North America. Free-tier BGP/IX services were marked "Currently Unavailable" at research time; no API, no pricing surfaced.
- **F4 Networks** — Popular low-end BGP host (free BGP/BYOIP, Arelion/HE upstreams). Ordering via store.f4.network on what is widely reported to be VirtFusion. VirtFusion's API is admin-only; F4 does not document customer API access, and BGP is provisioned manually. Cheapest BGP-capable VPS in the batch.
- **Skywolf Cloud** — Fremont + Hong Kong. Supports BGP communities/passthrough, but its own terms state **BGP sessions may not be used for commercial purposes** — a hard blocker for a production second provider regardless of automation. No public API.
- **SolidNetDC / SolidVPS** — Established SoCal MSP, LA colocation (CoreSite). Cheap cloud VPS, but no public API or documented BGP-provisioning workflow was found; treat as panel + ticket.
- **IonSwitch** — Single US site (Coeur d'Alene, ID). Uses Virtualizor. Advertises free IP announcements / BGP sessions, but no customer API docs and no documented self-service BGP turn-up.

## Verdict
**Best fully-code-driven candidate in this batch: none.**

Every provider here is **PANEL-ONLY** for programmatic compute: none publish a customer-facing compute API or ship a
Terraform/OpenTofu provider, and BGP sessions are turned up via panel-request or support ticket rather than an API.
Sucura Networks and F4 Networks are the closest in spirit (instant provisioning + first-class BGP) but still fail the
"public compute API / TF provider" bar. For a genuinely code-driven second BGP provider alongside Vultr, look outside
this batch (e.g., providers with real public APIs + TF providers such as Latitude.sh, or continue evaluating other
candidates).

## Sources
- Sucura Networks: https://sucuranetworks.ca/ , https://sucuranetworks.ca/vms , https://sucuranetworks.ca/toronto-vps , https://bgp.tools/as/398999
- Tier.Net: https://tier.net/vps , https://www.tier.net/infrastructure , https://billing.tier.net/knowledgebase/37/BGP-Communities.html , https://bgp.tools/as/397423
- Xentain Solutions: https://xentain.com/network-services , https://www.peeringdb.com/net/34191 , https://bgp.tools/as/15353
- F4 Networks: https://lowendbox.com/blog/f4-networks-24-year-1gb-vps-in-kansas-city-6-mo-4gb-vps-free-bgp-many-other-services/ , store.f4.network , https://bgp.tools/as/21738
- Skywolf Cloud: https://www.peeringdb.com/asn/7720 , https://bgp.tools/as/7720
- SolidNetDC / SolidVPS: https://solidnetdc.com/ , https://www.solidvps.com/ , https://www.peeringdb.com/net/33974
- IonSwitch: https://www.ionswitch.com/ , https://www.ionswitch.com/vps , https://bgp.tools/as/16584
- VirtFusion API/self-service (panel context): https://docs.virtfusion.com/api/ , https://docs.virtfusion.com/guides/self-service/
