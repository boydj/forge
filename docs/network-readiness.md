# AS215520 network readiness (public state)

Queried: 2026-09-10T04:00Z–04:10Z (UTC), read-only, from a shell with `whois`, `dig`, `curl`, `jq`.
Machine-readable facts with per-fact source URLs: `infra/network/public-state.yaml`.

Sources: RIPE DB REST (`rest.db.ripe.net`), RIPE RDAP, RIPEstat (`stat.ripe.net`), ARIN RDAP/whois, RADB whois, PeeringDB API, rpki-client console (`console.rpki-client.org/rpki.json`), Spamhaus DROP/DROPv6/ASN-DROP, ARDC geofeed, bgp.tools, bgp.he.net, Verisign RDAP, public DNS (1.1.1.1 plus authoritative servers).

## TL;DR

| | IPv6 | IPv4 |
| --- | --- | --- |
| Prefix | `2a0f:85c1:368::/48` | `44.32.58.0/24` |
| Registry | RIPE NCC, **ASSIGNED (PA)** under Inferno Communications' `2a0f:85c0::/29` | ARIN, part of ARDC's `44.0.0.0/9` (AMPRNet / 44Net); the /24 itself is an ARDC portal allocation, **not visible in any RIR whois** |
| Announced today | **Yes**, 320/320 RIS v6 peers (100 %), origin AS215520 via AS835 (GoCodeIT / Xenyth) | **No.** Withdrawn 2026-08-27 15:26 UTC (was via Vultr AS20473 since 2024-03-30). 1/326 RIS peers still hold a stale path |
| RPKI | **Valid**: ROA `2a0f:85c1:368::/48` maxLength 48 origin AS215520 (RIPE TA) | **Unknown/not-found**: no ROA for the /24, nor for any covering prefix (`44.0.0.0/8`, `/9`, `44.32.0.0/16`) |
| IRR | `route6` in RIPE (JOSHBOYD-MNT) and RADB (MAINT-AS20473, Vultr) | `route` in RADB, mnt-by MAINT-ARDC, descr `KN4LJL` |
| Reverse DNS | **Not delegated** (`8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` NXDOMAIN at RIPE; no `domain` object) | **Not delegated** (`58.32.44.in-addr.arpa` NXDOMAIN at ARDC's `ns.ardc.net`) |
| Blocklists | Not in Spamhaus DROPv6 | Not in Spamhaus DROP; AS215520 not in ASN-DROP |
| Geofeed | None on the inet6num | ARDC feed lists `44.32.58.0/24,US,,,` (country only) |

Top blockers for the project's "anycast IPv4 /24 + IPv6 /48 from AS215520" goal:

1. The IPv4 /24 is currently **not announced anywhere** and has **no ROA**. Only ARDC can create a ROA (the space is ARIN-registered to ARDC), and any new upstream (Vultr BYOIP, etc.) will want an ARDC LOA. Both go through the ARDC portal / ARDC support, i.e. a human on ARDC's side.
2. **Reverse DNS is not delegated for either prefix.** The /48 needs a RIPE `domain` object that only `INFERNO-MNT` can create (the inet6num carries no `mnt-domains`); the /24 needs an NS delegation entered in the ARDC portal.
3. **The /48 is PA space sub-assigned by the sponsoring LIR**, not PI. It is "portable" across upstreams (route6 + ROA + `mnt-routes` are the operator's) but it is tied to the Inferno relationship. Not a blocker today, a strategic risk.
4. No public **DNS records exist yet** for `git.`, `nodes.`, etc.; `gemini.as215520.net` points at Vultr-owned addresses (`207.246.80.52`, `2001:19f0:4000:3516:...`), not at either prefix. DNSSEC is signed by Cloudflare but the **DS is not published** at the registrar.

None of these are surprising for a hobbynet; all are fixable, but items 1 and 2 need third parties.

## 1. ASN registration

Source: `https://rest.db.ripe.net/search.json?query-string=AS215520&flags=no-referenced&flags=no-filtering`, `https://rdap.db.ripe.net/autnum/215520`.

| Attribute | Value |
| --- | --- |
| RIR | RIPE NCC (as-block `AS215130 - AS219547`, "RIPE NCC ASN block", created 2026-06-02) |
| aut-num | `AS215520` |
| as-name | `JOSH-BOYD` |
| org | `ORG-JB158-RIPE` — `JOSHUA BOYD`, org-type `OTHER`, country `US`, address "PMB 131, 43330 Junction Plz Ste 164, Ashburn, VA 20147", e-mail `abuse@unplanks.com`, mnt-by `JOSHBOYD-MNT`/`JOSHBOYD-SHARED-MNT`, mnt-ref includes `INFERNO-MNT`, last-modified 2026-05-13 |
| sponsoring-org | `ORG-ICL64-RIPE` — Inferno Communications Ltd, GB LIR (reg-nr 11502705), `noc@inferno.net.uk`, `abuse@inferno.net.uk`, +44 3337 999 999 |
| admin-c / tech-c | `JB21841-RIPE` (Joshua Boyd, same Ashburn address, phone +1 513 375 0157) |
| abuse-c | Inherited from the org: `ACRO55456-RIPE`, abuse-mailbox `abuse@unplanks.com` (no abuse-c on the aut-num itself, which is normal) |
| status | `ASSIGNED` (not legacy; 32-bit ASN issued 2024) |
| mnt-by | `RIPE-NCC-END-MNT`, `JOSHBOYD-MNT` (auth: SSO; upd-to `boydjd@jbip.net`) |
| created / last-modified | 2024-02-12T15:41:50Z / 2024-02-12T15:41:50Z (never edited since creation) |
| import/export | `from AS209735 accept ANY` / `to AS209735 announce AS215520` (Lagrange Cloud Technologies); `from AS207841 accept ANY` / `to AS207841 announce AS215520` (Inferno Communications) — **stale**: neither ASN is an observed neighbour today (see section 10) |
| remarks / descr | none |
| RIPEstat holder string | `JOSH-BOYD JOSHUA BOYD` |

Maintainers in play: `JOSHBOYD-MNT` (aut-num, route6, org, person, role) and `JOSHBOYD-SHARED-MNT` (route6, org, role). Objects maintained by them: aut-num AS215520, route6 `2a0f:85c1:368::/48`, organisation ORG-JB158-RIPE, role ACRO55456-RIPE, person JB21841-RIPE. No `as-set`, `route-set`, `domain`, or `inetnum`/`inet6num` object is maintained by either.

## 2. Prefixes associated with AS215520

Source: RIPEstat `routing-history`, `announced-prefixes`, `routing-status` for `AS215520`; RIPE DB inverse `origin` lookup; RADB `-i origin AS215520`.

| Prefix | Where it comes from | Announced by AS215520 | Now |
| --- | --- | --- | --- |
| `2a0f:85c1:368::/48` | RIPE, PA sub-assignment from Inferno's `2a0f:85c0::/29` | since 2024-02-11/16 continuously | announced, 100 % RIS visibility |
| `44.32.58.0/24` | ARDC 44Net allocation (ARIN `44.0.0.0/9` → ARDC) | 2024-03-30 → 2026-08-27 (last withdrawal 15:26:17 UTC), 325–365 full RIS peers throughout | withdrawn; one stale RIS path at RRC16 |
| `2a12:bec4:19a3::/48` | RIPE, ScaleBlade → Evolus-IX (`ORG-EISG7-RIPE`), route6/ROA now point at AS215120 | only 2025-05-12 → ~2025-06-04 | not ours; historical, ignore |

No other prefix has ever been originated by AS215520 in RIS data. PeeringDB claims `info_prefixes4: 100` / `info_prefixes6: 100`, which is an overstatement.

## 3. Per-prefix registration and routing detail

### 3a. `2a0f:85c1:368::/48`

Source: `https://rest.db.ripe.net/search.json?query-string=2a0f:85c1:368::/48&flags=all-less`.

| Attribute | Value |
| --- | --- |
| inet6num | `2a0f:85c1:368::/48`, netname `JOSH-BOYD`, country **GB** (operator is in the US; cosmetic) |
| status | `ASSIGNED` — a PA assignment inside `2a0f:85c0::/29` (`UK-INFERNOLAN-20191115`, `ALLOCATED-BY-RIR`, org ORG-ICL64-RIPE). **Not ASSIGNED PI.** |
| org | none on the /48 |
| admin-c / tech-c | `JB21841-RIPE` |
| abuse-c | `ICA28-RIPE` "Inferno Communications End User Abuse", abuse-mailbox `abuse-sponsor@inferno.net.uk` (Inferno's sponsored-resource abuse role, not the operator's `abuse@unplanks.com`) |
| mnt-by | `INFERNO-MNT` only — the operator cannot edit the inet6num |
| mnt-routes | `JOSHBOYD-MNT` — the operator can create/modify `route6` objects (and did) |
| mnt-lower / mnt-domains | absent — the operator cannot create `domain` (reverse DNS) objects underneath |
| geofeed | none |
| created / last-modified | 2024-02-09T17:03:46Z / 2024-03-16T18:23:05Z |
| Announced | yes; RIPEstat `routing-status`: first seen 2024-02-16, 320/320 RIS v6 peers; 353 looking-glass paths |
| Upstreams observed | 352/353 paths end `... 835 215520` (AS835 GoCodeIT Inc = Xenyth Cloud, Toronto); 1 path `... 62513 215520` (AS62513 GoCodeIT Inc). Most common full paths: `6939 835 215520`, `24482 6939 835 215520`, `8218 6939 835 215520`, `9002 3257 835 215520` |
| Neighbours (RIPEstat) | AS835 (v6, 357 peers), AS62513 (v6), AS205794 "RTTW-AS JINZE YANG" (v6, likely IX/RS peer), AS20473 (v4 only, stale) |
| RPKI | **valid** — ROA `2a0f:85c1:368::/48` maxLength 48 origin AS215520, TA RIPE NCC (RIPEstat routinator; rpki-client console concurs, chain `expires` 2026-09-10T21:04:27Z which is the manifest/CRL horizon RIPE refreshes continuously, not a ROA end date). No other ROA covers this prefix or the parent `/29` |
| IRR | RIPE `route6` (mnt-by JOSHBOYD-MNT, JOSHBOYD-SHARED-MNT, created 2024-02-17); RADB `route6` descr "Vultr Customer Route" mnt-by MAINT-AS20473 (2025-08-31, created by Vultr for a BGP session). bgp.tools shows IRR expected ASN matches |
| Reverse DNS | not delegated: `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` → NXDOMAIN, SOA from `0.a.2.ip6.arpa` (RIPE). No RIPE `domain` object. Sibling /48s in `2a0f:85c1::/32` do have domain objects, typically mnt-by `INFERNO-MNT` plus the customer's maintainer |
| Blocklists | not in Spamhaus DROPv6 (2026-09-09 list) |

Consequence of PA status: the `/48` lives inside Inferno's allocation. If the sponsoring/LIR relationship with Inferno ends, the /48 is returned. Routing-wise it is portable (operator-held `mnt-routes`, own route6, own ROA), but it is not RIR-portable.

### 3b. `44.32.58.0/24`

Source: `https://rdap.arin.net/registry/ip/44.32.58.0/24`, `whois -h whois.arin.net 'n 44.32.58.0'`, `whois -h whois.radb.net -i origin AS215520`, RIPEstat `routing-status`/`routing-history`/`looking-glass`/`bgp-updates` for `44.32.58.0/24`.

| Attribute | Value |
| --- | --- |
| Registry object | ARIN returns only the parent: `NET-44-0-0-0-1` "AMPRNET", `44.0.0.0/9` + `44.128.0.0/10`, Direct Allocation (1992-07-01, updated 2024-11-07) to **Amateur Radio Digital Communications (ARDC)**, San Diego CA. Remark: `Geofeed https://portal.ampr.org/storage/geofeed.csv` |
| Status of the /24 | ARDC "44Net" allocation to the operator (RADB descr `KN4LJL`, an amateur callsign). Not registered in any RIR whois as a separate object; ARDC keeps it in its own portal (login required; `whois.ampr.org` was unreachable from this host: "Network is unreachable") |
| Contacts (ARIN, parent) | abuse `john@ardc.net` (BURWE9-ARIN), admin `rosy@ardc.net`/`noc@ardc.net`/`bdale@ardc.net`, tech `adam@ardc.net` |
| mnt-by / mnt-routes | n/a in ARIN. RADB route object is **mnt-by MAINT-ARDC** — the operator cannot edit it directly; ARDC's portal generates it |
| Announced | **No** (RIPEstat `routing-status`: 1/326 RIS v4 peers, i.e. one stale path). `announced-prefixes` shows the last announcement window ending 2026-08-27T08:00; `bgp-updates` shows 7,939 updates 2026-08-20→08-27 with the final withdrawal at **2026-08-27T15:26:17Z**. Since 2026-09-04 only 1.0 full-peer-equivalent sees it |
| Stale path | RRC16 (Miami), peer `198.32.243.133` (AS199524 G-Core), `199524 12956 3257 20473 215520`, last updated 2026-08-26T01:30 |
| Historical upstream | AS20473 (Vultr) exclusively: dominant paths `12779 7195 20473 215520`, `25091 7195 20473 215520`, `7195 20473 215520`, `8218 6461 3356 20473 215520`. Visibility 2024-03-30→2026-08-22 was 325–365 full RIS peers |
| Covering route | `44.0.0.0/9` origin AS7377 (UCSD, on behalf of ARDC) — traffic for the /24 currently lands at UCSD's 44Net gateway |
| RPKI | **not-found / unknown**: no ROA for `44.32.58.0/24`, none for `44.32.0.0/16`, `44.0.0.0/9` or `44.0.0.0/8` (RIPEstat rpki-roas, rpki-client dump 2026-09-10T04:04Z). Nothing makes an AS215520 announcement invalid today, but nothing protects it either |
| IRR | RADB `route: 44.32.58.0/24 origin: AS215520 descr: KN4LJL mnt-by: MAINT-ARDC` (last-modified 2026-04-17). No RIPE/ALTDB/other copies. Covering `44.0.0.0/9 origin AS7377` also in RADB |
| Reverse DNS | not delegated: `32.44.in-addr.arpa` is served by ARDC (`ns.ardc.net`, `ns2.us.ardc.net`, `ns1.de.ardc.net`, `a.gw4.uk`; SOA serial 2026090906); `58.32.44.in-addr.arpa` → NXDOMAIN from `ns.ardc.net`; no PTRs for .1/.2/.10/.100/.254 |
| Geofeed | ARDC's feed contains `44.32.58.0/24,US,,,` (also `44.32.0.0/16,US` and `44.0.0.0/9,US`) — country-level only |
| Blocklists | not in Spamhaus DROP (2026-09-09); AS215520/AS20473/AS835 not in ASN-DROP |

Note on "portable": this /24 is portable in the practical sense (ARDC allows direct BGP announcement from the holder's ASN with an ARDC LOA), but it is not an RIR assignment to the operator and ARDC's terms govern its use (non-commercial amateur-radio purposes).

## 4. RPKI summary

| Prefix | ROA | maxLength | Origin | TA | Status for AS215520 | Conflicting ROA? |
| --- | --- | --- | --- | --- | --- | --- |
| `2a0f:85c1:368::/48` | yes | 48 (exact) | AS215520 | RIPE NCC | valid | none |
| `44.32.58.0/24` | **none** | — | — | — | not-found (unknown) | none (no covering ROA at all) |

maxLength 48 on the /48 means no more-specific can ever be RPKI-valid; that is the right setting for a single anycast /48. ROA validity end dates (EE certificate notAfter) are not exposed by RIPEstat or the rpki-client dump; the RIPE hosted-RPKI dashboard shows them. Cloudflare's `rpki.cloudflare.com/api/v1/validation` returned 404 and was not used.

## 5. IRR summary

| Object | Source | mnt-by | Notes |
| --- | --- | --- | --- |
| `route6 2a0f:85c1:368::/48 origin AS215520` | RIPE | JOSHBOYD-MNT, JOSHBOYD-SHARED-MNT | authoritative; matches inet6num `mnt-routes` |
| `route6 2a0f:85c1:368::/48 origin AS215520` | RADB | MAINT-AS20473 | "Vultr Customer Route", auto-created by Vultr |
| `route 44.32.58.0/24 origin AS215520` | RADB | MAINT-ARDC | ARDC-generated |
| `aut-num AS215520` | RIPE (mirrored in RADB) | RIPE-NCC-END-MNT, JOSHBOYD-MNT | import/export stale |
| as-set | **none** | — | `AS215520:AS-ALL`, `AS-JOSHBOYD`, `AS215520:AS-SET` all absent from RADB and RIPE; no as-set lists AS215520 as a member |

IX route-server as-sets bgp.tools associates with the AS: `AS-ONIX`, `AS-4IXP`, `AS-BGP-EXCHANGE-LONDON-RS`, `AS-BGPEXCHANGE-PEERS`, `AS-FREMIX`, `AS-LAGRANGE-LON`, `AS-HURRICANEV6`.

## 6. PeeringDB

Source: `https://www.peeringdb.com/api/net?asn=215520`, `/api/netixlan?asn=215520`, `/api/org/37423`, `/api/poc?net_id=35354`.

| Field | Value |
| --- | --- |
| net id / org id | 35354 / 37423 ("JOSHUA BOYD"; org address, city, country all empty) |
| name / website | JOSHUA BOYD / `https://as215520.net` |
| irr_as_set | **empty** |
| info_type(s) | Educational/Research, Network Services; scope Global; unicast yes; IPv6 yes |
| info_prefixes4 / 6 | 100 / 100 (**overstated**; real: 1 / 1) |
| policy | Open, locations Not Required, contracts Not Required |
| POCs | none visible via the API (`poc_set` empty even authenticated-as-anonymous); `poc_updated` 2024-03-02 |
| facilities | 0 |
| IXPs (3, all IPv6-only, all RS peers, operational) | BGP.Exchange - London `2a0e:8f01:1000:10::102` 1G; ONIX `2001:504:125:e1::bba` 100M; BGP.Exchange - Toronto `2a0e:8f01:1000:33::12b` 1G |
| rir_status | ok (2024-06-26) |
| created / updated | 2024-02-14 / 2024-04-02 (netixlan updated 2026-09-08) |

The record exists, which is good; it has no public contacts, no as-set and wrong prefix counts.

## 7. Reverse DNS

| Zone | Delegated to | Registry object |
| --- | --- | --- |
| `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` (the /48) | nothing (NXDOMAIN at RIPE `pri.authdns.ripe.net`) | no RIPE `domain` object; creating one needs `INFERNO-MNT` authorisation (inet6num has `mnt-by INFERNO-MNT`, no `mnt-domains`/`mnt-lower`) |
| `1.c.5.8.f.0.a.2.ip6.arpa` (Inferno's `2a0f:85c1::/32`) | not delegated as a whole; RIPE serves per-/48 delegations from `domain` objects (dozens exist for sibling customers) | — |
| `58.32.44.in-addr.arpa` (the /24) | nothing (NXDOMAIN, authoritative answer from `ns.ardc.net`) | ARDC portal controls NS records inside `32.44.in-addr.arpa` |
| `44.in-addr.arpa` | ARIN (`r/u/x/y/z.arin.net`, `arin.authdns.ripe.net`) → `32.44` delegated to ARDC | — |

## 8. Geofeed

* `2a0f:85c1:368::/48`: no `geofeed:` attribute or remark on the inet6num (and the operator cannot add one; Inferno must).
* `44.32.58.0/24`: covered by ARDC's RFC 8805 feed at `https://portal.ampr.org/storage/geofeed.csv` (referenced from the ARIN parent object per RFC 9092) with `44.32.58.0/24,US,,,`. Region/city are blank; changing them is an ARDC portal setting.

## 9. Contact and abuse sanity

| Contact | Where | DNS/mail check |
| --- | --- | --- |
| `abuse@unplanks.com` | RIPE org e-mail and abuse-mailbox (ACRO55456-RIPE) | `unplanks.com` MX → Cloudflare Email Routing (`route1/2/3.mx.cloudflare.net`); deliverability of the mailbox itself not verifiable from outside |
| `abuse-sponsor@inferno.net.uk` | abuse-c on the /48 inet6num | MX → Microsoft 365 |
| `boydjd@jbip.net` | `upd-to` on both maintainers | MX → Google |
| `john@ardc.net`, `noc@ardc.net` | ARIN abuse/admin for 44/9 | MX `mail.ardc.net` |
| +1 513 375 0157 | person JB21841-RIPE | not tested |

A real person/address/phone is present in RIPE whois; the abuse mailbox uses a different domain (`unplanks.com`) than the network (`as215520.net`) and the maintainer (`jbip.net`), which is legitimate but worth being deliberate about. Abuse reports for the /48 will go to Inferno first, for the /24 to ARDC first.

## 10. Current announcements and visibility

| Item | Value |
| --- | --- |
| Prefixes originated now | `2a0f:85c1:368::/48` only |
| RIS visibility | v6: 320/320 peers (100 %); v4: 0 prefixes (1 stale peer for the /24) |
| Transit upstreams observed | AS835 GoCodeIT Inc (Xenyth Cloud, Toronto) — carries essentially all v6 paths; AS62513 GoCodeIT Inc — 1 path |
| IX/peer neighbours observed | AS205794 (RTTW-AS); bgp.tools counts 27 peers, tags "Personal ASN", "IPv6 only" |
| Last IPv4 transit | AS20473 Vultr (until 2026-08-27) |
| Declared but unobserved | AS209735 Lagrange Cloud (London PoP on the website; `AS-LAGRANGE-LON`), AS207841 Inferno |
| Website `https://as215520.net` | static page (last-modified 2024-02-20), Cloudflare-proxied (`x-pop: CA-TOR`): "hobbynet based in Ashburn, VA"; PoPs: Lagrange Cloud (London), Xenyth Cloud (Toronto); IX table lists only ONIX |

### DNS for `as215520.net`

Source: `dig @1.1.1.1`, `https://rdap.verisign.com/net/v1/domain/as215520.net`.

| Item | Value |
| --- | --- |
| Registrar / dates | NameCheap, Inc.; registered 2024-02-15, expires 2027-02-15, status clientTransferProhibited |
| Nameservers | `fay.ns.cloudflare.com`, `kip.ns.cloudflare.com` (Cloudflare; SOA `dns.cloudflare.com`) |
| DNSSEC | zone is signed (DNSKEY alg 13, ZSK+KSK present, RRSIGs returned) but **no DS at the parent** (`delegationSigned: false`); resolvers do not set AD |
| apex A/AAAA | `104.21.74.245`, `172.67.207.242`, `2606:4700:3033::6815:4af5`, `2606:4700:3037::ac43:cff2` (Cloudflare proxy) |
| `www` | same Cloudflare proxy addresses |
| `gemini` | `207.246.80.52`, `2001:19f0:4000:3516:5400:6ff:fe9a:c207` — Vultr-owned addresses, **not in the /24 or /48**; HTTPS on it did not answer |
| `git`, `nodes`, `lg`, `ns1`, `ns2`, `mail`, `bgp`, `rpki`, `status`, `api`, `forge`, `whois`, `ipv6`, `vpn` | do not exist; no wildcard |
| MX | Cloudflare Email Routing (`route1/2/3.mx.cloudflare.net`) |
| TXT | `v=spf1 include:_spf.mx.cloudflare.net include:_spf.google.com ~all`; `_dmarc` `p=none` with Cloudflare rua |
| CAA | none |

## Readiness matrix

Desired state = `docs/architecture.md`: the same services reachable over an anycast IPv4 /24 and IPv6 /48 originated by AS215520 from several PoPs, announced/withdrawn by BIRD under health control.

| # | Item | Current state | Desired state | Who can change it | Blocker? |
| --- | --- | --- | --- | --- | --- |
| 1 | ASN registration | RIPE, ASSIGNED, sponsored by Inferno, contacts present | same | — | no |
| 2 | aut-num import/export | lists AS209735, AS207841 (not current) | list actual upstreams (AS835, AS62513, AS20473 or whichever are used) | operator (JOSHBOYD-MNT, RIPE SSO) | no (cosmetic, helps IRR-based filtering audits) |
| 3 | as-set | none | `AS215520:AS-ALL` in RIPE with `members: AS215520`; referenced from PeeringDB | operator | no, but needed before asking IX route servers/upstreams to filter on an as-set |
| 4 | IPv6 /48 registration | ASSIGNED PA under Inferno /29, country GB, mnt-by INFERNO-MNT | acceptable; optionally country US, `org:` and `geofeed:` set; longer term consider PI or own allocation | Inferno (inet6num), RIPE NCC for PI | no (risk only) |
| 5 | IPv6 /48 announcement | announced, 100 % visibility, one real upstream (AS835) | announced from every PoP (anycast) | operator + each PoP provider | no for the first PoP; more PoPs need more v6 transit |
| 6 | IPv6 ROA | valid, maxLength 48 | same | Inferno (LIR) if it ever needs changing | no |
| 7 | IPv6 route6 | RIPE + RADB present | same | operator | no |
| 8 | IPv6 reverse DNS | not delegated, no domain object | `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` delegated to Cloudflare (or the project's NS) | **Inferno** (INFERNO-MNT) must create the domain object or add `mnt-domains: JOSHBOYD-MNT` | **yes** for PTRs (mail/reputation), not for reachability |
| 9 | IPv4 /24 holding | ARDC 44Net allocation, RADB route by MAINT-ARDC | same, plus written LOA naming AS215520 and each upstream | operator via ARDC portal; ARDC issues | **yes** for onboarding at any upstream that asks for an LOA (Vultr does) |
| 10 | IPv4 /24 announcement | **withdrawn since 2026-08-27** | announced from every PoP | operator + upstreams (Vultr BGP session or other v4 transit; AS835 currently carries v6 only) | **yes** |
| 11 | IPv4 ROA | none (unknown) | ROA `44.32.58.0/24` maxLength 24 origin AS215520 under ARIN TA | **ARDC** (ARIN hosted RPKI on ARDC's cert); request through ARDC portal/support | **yes** in practice: many upstreams and Vultr BYOIP checks want valid RPKI; also required to stop hijacks |
| 12 | IPv4 reverse DNS | not delegated | `58.32.44.in-addr.arpa` NS → Cloudflare/project NS | operator via ARDC portal (ARDC executes) | **yes** for PTRs |
| 13 | IPv4 geofeed | ARDC feed: US only | US / VA / Ashburn (or the anycast policy you choose) | operator via ARDC portal | no |
| 14 | Blocklists | clean (Spamhaus DROP/DROPv6/ASN-DROP) | clean | — | no |
| 15 | PeeringDB | exists; no POCs, no as-set, prefix counts 100/100, no facilities, IPv6-only IX entries | POCs (Abuse/NOC/Policy), `irr_as_set RIPE::AS215520:AS-ALL`, prefixes 1/1, IPv4 IX addresses where available | operator (PeeringDB account) | no |
| 16 | Domain DNSSEC | signed, no DS | DS published at Namecheap | operator | no (recommended) |
| 17 | Service DNS records | only apex/www/gemini; gemini points at Vultr IPs | `git.`, `gemini.`, `nodes.` etc. on anycast addresses; CAA records | operator (Cloudflare, `infra/opentofu/modules/cloudflare-dns`) | no (blocked on 5/10 producing addresses) |
| 18 | Abuse contact | `abuse@unplanks.com` (Cloudflare Email Routing) | monitored mailbox, ideally also `abuse@as215520.net` alias | operator | no |

## Exact operator actions

Third-party-dependent (start these first; they have the longest lead time):

1. **ARDC portal (`portal.ampr.org`) for `44.32.58.0/24`**:
   a. Request/confirm an RPKI ROA: prefix `44.32.58.0/24`, maxLength `24`, origin `AS215520`. ARDC creates it under ARIN's hosted RPKI; the operator cannot do this in any RIR portal. Verify afterwards with `https://stat.ripe.net/data/rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0/24` (expect `valid`).
   b. Obtain a current LOA for `44.32.58.0/24` naming AS215520 and the upstream(s) that will carry it (Vultr AS20473 and any other v4 transit at each PoP).
   c. Set the reverse-DNS nameservers for the /24 so ARDC publishes `58.32.44.in-addr.arpa NS` records pointing at the chosen DNS (Cloudflare `fay/kip.ns.cloudflare.com` if the reverse zone is hosted there). Verify with `dig NS 58.32.44.in-addr.arpa @ns.ardc.net`.
   d. Optionally set region/city in the ARDC geofeed entry.
   e. Confirm the allocation's expiry/renewal terms and that anycast/commercial-adjacent use of a 44Net block is within ARDC's acceptable-use policy.
2. **Inferno Communications (sponsoring LIR, `noc@inferno.net.uk`) for `2a0f:85c1:368::/48`**:
   a. Ask them to create `domain: 8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` with `nserver:` set to the chosen DNS servers and `mnt-by: INFERNO-MNT, JOSHBOYD-MNT` (or add `mnt-domains: JOSHBOYD-MNT` to the inet6num so the operator can manage it). Host the zone in Cloudflare first so the delegation resolves immediately.
   b. Optionally ask them to set `country: US`, add `org: ORG-JB158-RIPE`, and a `geofeed:` attribute on the inet6num.
   c. Confirm the terms under which the PA /48 is held; decide whether a PI /48 (RIPE, via a sponsoring LIR) is worth pursuing so the v6 prefix is not tied to one LIR.
3. **Upstreams**: re-establish IPv4 transit. The only historical carrier is Vultr AS20473 (BGP session with the /24 as customer route; Vultr also created the RADB route6). Provide the ARDC LOA and, once the ROA exists, expect RPKI checks to pass. For anycast, secure v4-capable transit at each PoP (AS835/Xenyth currently carries only v6 for this AS).

Self-service, in the RIPE DB (`JOSHBOYD-MNT`, SSO auth):

4. Create `as-set: AS215520:AS-ALL` with `members: AS215520`, `mnt-by: JOSHBOYD-MNT`.
5. Update `aut-num: AS215520` import/export lines to the real upstreams; keep `mp-import/mp-export` for v6. Consider adding `remarks:` with the abuse mailbox and website.
6. Nothing to do for the route6 or the v6 ROA; keep maxLength 48.

Self-service, elsewhere:

7. PeeringDB (net 35354): set `irr_as_set` to `RIPE::AS215520:AS-ALL`, `info_prefixes4: 1`, `info_prefixes6: 1`, add public Abuse/Technical/Policy POCs, add facilities once known, add IPv4 IX addresses once assigned (ONIX, BGP.Exchange).
8. Cloudflare DNS (`as215520.net`): add `git`, `nodes` and other service records on the anycast addresses once announced; move `gemini` off the Vultr-owned addresses; add CAA; enable DNSSEC and publish the DS at Namecheap; create the two reverse zones (`58.32.44.in-addr.arpa`, `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa`) so delegations in steps 1c/2a resolve.
9. Website: refresh the PoP/IX table (three IXes in PeeringDB, only ONIX listed) and the last-modified 2024 content.
10. Abuse handling: make sure `abuse@unplanks.com` is actually routed and read; consider aliasing `abuse@as215520.net`.

## What could not be determined

* ARDC portal details for `44.32.58.0/24` (allocation date, expiry, existing LOA/ROA requests, DNS settings): the portal requires login and `whois.ampr.org` was unreachable from this host ("Network is unreachable").
* Whether PeeringDB has POCs with non-public visibility (the anonymous API shows none).
* ROA EE-certificate validity dates (not exposed by RIPEstat or the rpki-client dump); the RIPE LIR/RPKI dashboard shows them. Cloudflare's validation API (`rpki.cloudflare.com/api/v1/validation`) returned 404.
* Whether `abuse@unplanks.com` is actually delivered/read (only MX presence was checked).
* Exact reason for the 2026-08-27 withdrawal of the /24 (Vultr BGP session ended or was reconfigured; inference only).
* bgp.tools and bgp.he.net were scraped from HTML without JavaScript; upstream/peer lists there are approximate and were used only to corroborate RIPEstat.
