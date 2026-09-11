# North America BGP VPS Providers — Batch A

Research for a **fully code-driven** second BGP provider alongside Vultr (North America only).

- **FULLY-CODE-DRIVEN** = public compute API (or TF provider) that can create AND destroy a VPS, AND BGP obtainable without a human ticket.
- **PARTIAL** = compute API exists but BGP is a ticket/manual (or panel click-ops), or compute API can't fully create/destroy.
- **PANEL-ONLY** = no usable public compute API.

Skeptical rule applied: an API is only "usable" if real docs exist showing create + destroy. Marketing "API" claims without docs = PARTIAL at best.

_Researched 2026-09-11._

| Provider | NA locations | Panel | Compute API (URL) | TF provider | BGP provisioning | ASN | Price | Verdict |
|---|---|---|---|---|---|---|---|---|
| **RackCorp** | Los Angeles CA (CoreSite LA2), Northern Virginia (CoreSite VA1); no Canada | Custom portal (portal.rackcorp.com) | **YES** — REST v2.8, create via `POST /order/create/server`; DELETE per Swagger. Auth = API UUID+secret in JSON body. [docs](https://wiki.rackcorp.com/books/help-and-support-en/page/rackcorp-rest-api) | No (only stale 3rd-party [section-io repo](https://github.com/section-io/terraform-provider-rackcorp)) | **Support ticket** (manual). Peer IPs (110.232.119.251/.252) + communities documented, but activation human-gated, not via API | AS56038 (APNIC/AU-registered) | Not published (portal/quote only) | **PARTIAL** |
| **BuyVM / FranTech** | Las Vegas NV, New York (Piscataway NJ); + Luxembourg (EU) | Stallion (custom) | **NO** — client API is boot/reboot/shutdown/info/rdns only; no create/destroy. [docs](https://wiki.buyvm.net/doku.php/clientapi) | No | ASN approval via **support ticket** (RIR abuse-contact code), then session self-service in Stallion; peer details in panel, not API | AS53667 (PONYNET) | Slice 512 ~$2/mo ($24/yr); BGP free | **PANEL-ONLY** |
| **HostUS** | Los Angeles, Dallas, Washington DC (+ Atlanta, Charlotte) | Custom "Breeze" + WHMCS billing | **NO** — Breeze is GUI-only, cannot create/destroy; new VPS only via WHMCS cart. No public API docs. [panel](https://hostus.us/panel.html) | No | **Support ticket / LOA** (undocumented; not in KB, not self-service) | AS7489 (HOSTUS-GLOBAL-AS) | KVM-0.5 $4.35/mo | **PANEL-ONLY** |
| **ARP Networks** | Los Angeles only (+ Frankfurt DE) | Custom portal (phoenix.arpnetworks.com) | **YES** — OpenAPI "Platform API" v1: `POST /api/v1/servers` (create), `DELETE /api/v1/servers/{uuid}` (destroy). Auth = `Authorization: Bearer arp_live_…`. [docs](https://arpnetworks.com/api/docs) | No | **Panel/manual** — $10/mo add-on; no BGP endpoint in API, no public docs (KB "no results"), transit page says "Contact Us to Order" | AS25795 | Smallest VPS $10/mo + BGP $10/mo ≈ $20/mo | **PARTIAL** |
| **Terabit Systems** | Montreal QC, Dallas TX, Kansas City MO (+ edge PoPs) | Custom "Terabit One" + WHMCS | **NO** — [docs.terabit.io](https://docs.terabit.io/) covers only Firewall + Game Servers; no VPS create/destroy endpoints | No | **Manual LOA ticket**; BGP included with IP Transit product; no API. [LOA](https://help.terabit.io/en/article/letter-of-authorization-loa-1qldxcj/) | AS1002 (Bytefilter LLC) | Smallest VPS $6.99/mo (no distinct BGP tier) | **PANEL-ONLY** |
| **Xenyth Cloud** | Toronto ON (Coloware, 151 Front St) | Bespoke custom (dashboard.xenyth.net) | **YES** — `POST …/create` (create service), `POST …/{sid}/delete` (delete). Auth = `Authorization: Bearer {key}`, **but key is issued via a support ticket** (one-time). [docs](https://docs.xenyth.net/docs/category/endpoints) | No | **Self-service, no ticket** — dashboard on Edge (e2.) VMs; ASN verify automated via RIPEstat abuse-contact email; peer/session details shown in panel. Not via a documented API. [BGP doc](https://docs.xenyth.net/docs/services/cloud/bgp) | AS62513 (GoCodeIT Inc) | VPS from $2.5/mo (s1.micro); smallest BGP-capable Edge e2.micro $5.85/mo | **PARTIAL** (borderline — best in batch) |
| **Valor Node** | St. Louis MO (primary VPS site); network PoPs Toronto, Dallas, Chicago | VirtFusion | **NO** (for customers) — VirtFusion's create/destroy is admin-side; Valor Node exposes no documented public customer compute API; new VPS via WHMCS/panel | No official (3rd-party [EZSCALE VirtFusion](https://github.com/EZSCALE/terraform-provider-virtfusion) TF provider needs admin API creds — not customer-usable) | **Support ticket** — BGP VM / add-on "on request" | AS33755 (Valor Holdings LLC) | BGP VM from $1.50/mo; VPS from $1.25/mo | **PANEL-ONLY** |

## Notes (one line each)

- **RackCorp** — Real documented compute REST API (create; delete via Swagger), but BGP is a manual ticket and VPS pricing is quote-only; Australian ASN, US colo in LA + N. Virginia. PARTIAL.
- **BuyVM/FranTech** — Well-known cheap BGP host, but Stallion's public API can't create/destroy and ASN approval needs a ticket; a "v2" full API is promised, not shipped. PANEL-ONLY.
- **HostUS** — Custom Breeze panel is view/manage only; provisioning is WHMCS cart and BGP is an undocumented LOA ticket — nothing code-driven. PANEL-ONLY.
- **ARP Networks** — Cleanest compute API in the batch (Bearer token, true create + destroy, no key-ticket), but BGP is a $10/mo panel/"contact us" add-on with no API or docs. PARTIAL.
- **Terabit Systems** — Genuine API exists but only for firewalls/game servers, not VPS; BGP/BYOIP is a manual LOA. (terabit.io — not terabitsystems.com, an unrelated hardware reseller.) PANEL-ONLY.
- **Xenyth Cloud** — Only provider with BOTH a documented create/destroy compute API AND self-service (no-ticket, automated) BGP with peer details surfaced in-panel; the two gaps are: API key issued via a one-time ticket, and BGP ordered through the dashboard rather than a documented API. Toronto-only for NA. Strongest candidate.
- **Valor Node** — VirtFusion panel with no customer-facing public compute API and BGP "on request" (ticket); cheapest BGP VM at $1.50/mo but not code-drivable. PANEL-ONLY.

## Best fully-code-driven candidate

**None strictly qualifies as FULLY-CODE-DRIVEN.** The closest / best candidate is **Xenyth Cloud** (Toronto, AS62513): it uniquely pairs a documented create + destroy compute API with self-service, no-ticket BGP (automated ASN verification, peer details shown in the panel). Its two blockers to a full pass are (1) the API key must be requested once via a support ticket, and (2) BGP sessions are ordered through the dashboard rather than a documented API endpoint. **ARP Networks** is the runner-up for compute automation (clean Bearer-token create/destroy API, no key ticket) but its BGP is a manual panel/"contact us" add-on with no docs.
