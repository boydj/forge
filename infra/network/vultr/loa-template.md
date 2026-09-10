# Letter of Authorization (LOA) template for Vultr BYOIP

Vultr requires an LOA when enabling BGP for customer-owned IP space
(`docs/research/vultr.md` section 1-2; `infra/providers/vultr/facts.yaml`
`bgp.enable_process.loa_required`). The wording Vultr expects is that the
resource holder authorises **Vultr (AS20473)** to announce the listed blocks.
Upload it as a PDF in the BGP request form (`bgp-request-checklist.md`).

Two letters, because the two prefixes have different holders of record:

| Prefix | Holder of record | Who signs |
| --- | --- | --- |
| `2a0f:85c1:368::/48` | assigned (PA) to Joshua Boyd / ORG-JB158-RIPE inside Inferno's `2a0f:85c0::/29`; route6 + ROA are the operator's | the operator signs; Inferno's countersignature is optional but strengthens it (ask `noc@inferno.net.uk` if Vultr pushes back) |
| `44.32.58.0/24` | ARDC (ARIN `NET-44-0-0-0-1`); allocated to the operator in the 44Net portal | ARDC issues or countersigns (see `infra/network/irr/ardc-request.md` item 4); if ARDC's practice is "holder signs, ARDC confirms on request", the operator signs and names ARDC as registry |

One combined letter listing both prefixes is acceptable to Vultr if both
signatures are on it; separate letters are simpler to obtain. Fill every
`{{...}}`, print to PDF on letterhead (a plain header with name/address is
fine for a private individual), sign, date. Keep the signed PDFs out of the
repository.

---

```
{{ORG_LETTERHEAD: name, postal address, e-mail, phone}}

{{DATE (YYYY-MM-DD)}}

To:   Vultr Holdings, LLC / The Constant Company, LLC (AS20473)
      Network Operations, bgp@vultr.com
Re:   Letter of Authorization - BGP announcement of IP address space

To whom it may concern,

I, {{SIGNER_FULL_NAME}}, {{SIGNER_TITLE, e.g. "resource holder" / "network operator"}}
of {{ORG_NAME, e.g. "Joshua Boyd (AS215520), ORG-JB158-RIPE"}}, confirm that
{{ORG_NAME}} is the registered holder / authorised user of the following
Internet number resources:

    Autonomous System Number:  AS215520  (RIPE NCC, org ORG-JB158-RIPE)

    IPv4 prefix:               44.32.58.0/24
                               (ARIN NET-44-0-0-0-1, Amateur Radio Digital
                               Communications; 44Net allocation to {{CALLSIGN}})
    IPv6 prefix:               2a0f:85c1:368::/48
                               (RIPE NCC, inet6num JOSH-BOYD, ASSIGNED PA
                               within 2a0f:85c0::/29, Inferno Communications Ltd)

I hereby authorize Vultr Holdings, LLC and The Constant Company, LLC
(Autonomous System AS20473) to announce the IP address blocks listed
above from any of Vultr's locations, to route traffic for them to the
Vultr Cloud Compute instances in account {{VULTR_ACCOUNT_EMAIL}}, and to
create or update IRR route/route6 objects and RPKI records referencing
AS20473 as needed for that purpose, for as long as the account remains
active and the resources remain registered to {{ORG_NAME}}.

The prefixes will be originated by AS215520 over BGP sessions from those
instances; AS20473 is authorised as the transit AS (external AS path
"20473 215520"). Corresponding RPKI ROAs authorise AS215520 as origin
with maxLength equal to the prefix length. Corresponding IRR objects:
RIPE route6 2a0f:85c1:368::/48 origin AS215520; RADB route 44.32.58.0/24
origin AS215520 (mnt-by MAINT-ARDC).

This authorization may be revoked in writing at any time by
{{ORG_NAME}}; it expires automatically on {{EXPIRY_DATE, e.g. one year
from date, or "termination of the allocation"}}.

Questions regarding this letter may be directed to
{{CONTACT_NAME}}, {{CONTACT_EMAIL}}, {{CONTACT_PHONE}}.


____________________________________
{{SIGNER_FULL_NAME}}
{{SIGNER_TITLE}}, {{ORG_NAME}}
{{DATE}}


{{OPTIONAL COUNTERSIGNATURE BLOCK - resource registry / LIR}}
Confirmed on behalf of {{REGISTRY_ORG: "Amateur Radio Digital Communications (ARDC)" | "Inferno Communications Ltd (LIR, ORG-ICL64-RIPE)"}}:

____________________________________
{{REGISTRY_SIGNER_NAME}}, {{REGISTRY_SIGNER_TITLE}}
{{DATE}}
```

---

## Field values for this project

| Placeholder | Value |
| --- | --- |
| `ORG_NAME` | Joshua Boyd (AS215520), ORG-JB158-RIPE |
| `SIGNER_FULL_NAME` | Joshua Boyd |
| `CALLSIGN` | KN4LJL |
| Postal address | PMB 131, 43330 Junction Plz Ste 164, Ashburn, VA 20147, US (matches RIPE person JB21841-RIPE) |
| `CONTACT_EMAIL` | boydjd@jbip.net (maintainer `upd-to`); abuse@unplanks.com for abuse |
| `CONTACT_PHONE` | +1 513 375 0157 |
| `VULTR_ACCOUNT_EMAIL` | the login e-mail of the Vultr account that will hold the BGP configuration |
| `EXPIRY_DATE` | one year from signing; renew when Vultr asks (they re-verify BYOIP periodically) |

Checks before uploading: the ASN, both prefixes and the contact details must
match the RIR records exactly (Vultr e-mails the RIR POC, `docs/research/vultr.md`
section 2: "confirmation email to the RIR POC the same day"). For the /24 that
POC is ARDC's, so warn ARDC that the mail is coming or ask them to sign.
