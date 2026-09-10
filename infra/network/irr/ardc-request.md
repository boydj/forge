# ARDC request for 44.32.58.0/24 (exact wording)

Everything about the IPv4 /24 that the operator cannot do alone goes through
ARDC (Amateur Radio Digital Communications), holder of `44.0.0.0/9` at ARIN.
Channel: the 44Net portal https://portal.ampr.org (allocation record for
`44.32.58.0/24`, callsign `KN4LJL`), falling back to a ticket to
`support@ardc.net` (ARIN contacts on the parent net: `noc@ardc.net`,
`adam@ardc.net`). Four asks, one ticket; number them so ARDC can tick them off.

Before sending: log in to the portal and check whether any of the four already
exists (a "BGP" tab with ROA/LOA state, a "DNS" tab with NS records). The
readiness scan could not see inside the portal. Reference the allocation and
callsign in the subject.

Subject: `44.32.58.0/24 (KN4LJL): route object confirmation, RPKI ROA, reverse DNS delegation, LOA for AS215520`

---

Hello ARDC,

I hold the 44Net allocation 44.32.58.0/24 (callsign KN4LJL, portal
allocation for Joshua Boyd). I announce it from my own ASN, AS215520
(RIPE NCC, org ORG-JB158-RIPE), as an anycast prefix from several
locations via Vultr (AS20473) as the first upstream. Please action the
following four items.

1. IRR route object (RADB, MAINT-ARDC): please confirm that

       route:   44.32.58.0/24
       origin:  AS215520
       descr:   KN4LJL
       mnt-by:  MAINT-ARDC
       source:  RADB

   stays as it is (it is correct today). No change is requested; I only
   ask that it is not removed or re-originated while the allocation is
   active. If ARDC also publishes route objects in ARIN's IRR, the same
   object there would be welcome.

2. RPKI ROA (ARIN hosted RPKI, ARDC's certificate for 44.0.0.0/9):
   please create exactly one ROA

       prefix:     44.32.58.0/24
       origin AS:  AS215520
       maxLength:  24

   No ROA for AS20473 is needed (my Vultr account is configured for
   AS215520 and Vultr's nightly RPKI check expects the customer ASN).
   Please do not create a covering ROA on a shorter prefix with
   maxLength 24; it would authorise AS215520 for the whole covering
   block. Currently there is no ROA at all, so the announcement
   validates as "unknown".

3. Reverse DNS: please delegate 58.32.44.in-addr.arpa to

       58.32.44.in-addr.arpa.  NS  fay.ns.cloudflare.com.
       58.32.44.in-addr.arpa.  NS  kip.ns.cloudflare.com.

   (the zone is already hosted there; the delegation will resolve as
   soon as the NS records are in ns.ardc.net). If the portal lets me
   set these myself, a pointer to the right page is enough.

4. Letter of Authorization: please issue (or countersign) an LOA on
   ARDC letterhead stating that Joshua Boyd / AS215520 is authorised to
   announce 44.32.58.0/24 and that Vultr Holdings LLC / The Constant
   Company, AS20473, is authorised to announce and route it on my
   behalf, from any of Vultr's locations, for the period of the
   allocation. Vultr requires this document (PDF) when enabling BGP for
   customer-owned space. A draft with the wording Vultr expects is
   attached (infra/network/vultr/loa-template.md). If ARDC prefers that
   the holder signs the LOA and ARDC only confirms the allocation on
   request, please say so and I will sign it myself and name ARDC as the
   registry of record.

Optional, no urgency: the geofeed entry for 44.32.58.0/24 may list
"US, VA, Ashburn"; and I confirm the block is used for a non-commercial
amateur-radio-community service (a Gemini/Titan/SSH code forge), so
please tell me if anycast use from a commercial cloud provider needs
any additional note on the allocation.

Thank you,
Joshua Boyd, KN4LJL
AS215520 / ORG-JB158-RIPE
boydjd@jbip.net, +1 513 375 0157
PMB 131, 43330 Junction Plz Ste 164, Ashburn, VA 20147, US

---

## After ARDC replies

| Item | Verify with | Expected |
| --- | --- | --- |
| 1 route | `whois -h whois.radb.net 44.32.58.0/24` | unchanged object, origin AS215520 |
| 2 ROA | `scripts/netcheck` row `rpki:44.32.58.0/24`; https://stat.ripe.net/data/rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0/24 | `valid`, maxLength 24 (allow a few hours for validators; RIPEstat's routinator refreshes hourly) |
| 3 rDNS | `dig NS 58.32.44.in-addr.arpa @ns.ardc.net`; `scripts/netcheck` row `dns:58.32.44.in-addr.arpa:NS` | the two Cloudflare NS |
| 4 LOA | PDF in hand | upload it in the Vultr BGP form (`infra/network/vultr/bgp-request-checklist.md`) |

Keep the LOA PDF outside the repository (it carries a signature); note its
location and date in the operator's password manager.
