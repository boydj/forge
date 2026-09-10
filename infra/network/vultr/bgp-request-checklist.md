# Vultr BGP + BYOIP request checklist (AS215520)

Human, one-time, console-only work. No OpenTofu/API resource exists for any
of it (`docs/research/vultr.md` section 8; `infra/providers/vultr/facts.yaml`
`bgp.api.terraform_resource: null`). Do it once per Vultr account; every
instance afterwards inherits the BGP enablement and the on-boarded prefixes.

## 0. Prerequisites (all must be true before opening the form)

- [ ] **One active Cloud Compute instance exists** on the account (Vultr rejects the form otherwise). Create ewr1 with `tofu apply` in `infra/opentofu/environments/dev` first (`docs/runbooks/network-bootstrap.md` step 5). Plan `vc2-1c-1gb` or `vhp-1c-1gb-*`; sandbox 512 MB plans are unverified for BGP.
- [ ] **Account instance limit**: new accounts start at 1 instance (`limits.new_account_instances`, unverified). One is enough for the request; raise it before the second POP via *Billing > Account limits > Request Limit Increase* (identity + use-case form, hours to days).
- [ ] **LOA PDF(s)** signed (`loa-template.md`): /48 signed by the operator, /24 issued or countersigned by ARDC.
- [ ] **RPKI**: /48 ROA valid (it is); /24 ROA requested from ARDC. Vultr's nightly check flags "Invalid ASN" if a ROA names a different origin than the account ASN; a missing ROA ("unknown") slows acceptance but is allowed. Better to wait for the ARDC ROA than to argue with the reviewer.
- [ ] **IRR**: RIPE route6 exists; RADB route (MAINT-ARDC) exists. Vultr accepts RADB/ARIN/RIPE/APNIC and would auto-create in RADB if nothing matched (nothing will be created here).
- [ ] **MD5 password** generated and stored *before* filling the form so the two copies cannot diverge:

  ```
  openssl rand -base64 24 | tr -d '/+=' | cut -c1-24      # letters+digits only; Vultr's form is picky about symbols
  scripts/secrets edit infra/secrets/dev.enc.yaml           # set: vultr_bgp_password: "<that value>"
  scripts/secrets get  infra/secrets/dev.enc.yaml vultr_bgp_password   # read back what the deploy will use
  ```

  (`scripts/secrets` has no `set` subcommand; `edit` opens the SOPS bundle in `$EDITOR`. The key name `vultr_bgp_password` is what `address-plan.yaml` `bgp.upstreams.vultr.md5_secret_ref` and `scripts/netgen`'s `__BGP_MD5__` placeholder expect. Use the same value for `infra/secrets/production.enc.yaml` later, or rotate through the Vultr console + `scripts/secrets rotate bgp`.)
- [ ] RIPE aut-num shows import/export for AS20473 (`infra/network/irr/aut-num-AS215520.rpsl`); reviewers look.
- [ ] PeeringDB record tidy (`infra/network/peeringdb/desired.yaml`); reviewers look here too.

## 1. The form

Console: **Products > Network > BGP > Get Started** -> https://console.vultr.com/bgp/setup/

| Field | Value |
| --- | --- |
| "I have my own IP space" | **on** (the other path assigns private AS64515 and only allows Vultr Reserved IPs) |
| ASN | `215520` |
| BGP password | the value stored in SOPS (`vultr_bgp_password`); this becomes the TCP-MD5 secret on every session |
| IP blocks | `44.32.58.0/24` and `2a0f:85c1:368::/48` (one line each; the limit is 5 per account; these are the smallest sizes Vultr accepts, /24 and /48) |
| Letter of Authorization | upload the PDF(s); if the form takes one file, merge both letters into one PDF |
| Route preference | **Default Only** (`bgp.import_policy: default-only`; the generated BIRD config accepts exactly `0.0.0.0/0` and `::/0` with `import limit 10` and never installs them; "Full Table" would trip the limit and shut the session) |
| Use case | see wording below |
| Locations | if asked: `ewr` (New Jersey) now, `ams` (Amsterdam) next; both have dynamic transit filtering (6-24 h). Anycast from several locations is explicitly allowed. |

Use-case text:

> Anycast for a small non-commercial code forge (Gemini/Titan/SSH, git.as215520.net) operated by AS215520 (RIPE). Both prefixes will be announced whole (/24 and /48, ROA maxLength = prefix length, origin AS215520) from one Cloud Compute instance per region, starting with New Jersey, then Amsterdam. Sessions: BIRD 2, TCP-MD5, multihop 2, Default Only. No more-specifics, no blackholing, no traffic engineering communities beyond 20473:6003 during maintenance drains. The IPv4 /24 is an ARDC 44Net allocation (LOA attached); the IPv6 /48 is RIPE PA space assigned to the operator (route6 + ROA in place).

Submit. The form returns a support-ticket reference; note it.

## 2. What to expect

| Stage | Timing | Signal |
| --- | --- | --- |
| Automatic checks (Spamhaus DROP, ROA) | immediate | form accepted or rejected with a reason |
| Human review by a Vultr representative | hours to 1-2 business days | ticket update; they may ask for the LOA again or for RIR POC confirmation |
| RIR POC confirmation e-mail | same day as review | e-mail to the RIR contact of each prefix (RIPE: JB21841-RIPE / `boydjd@jbip.net`; ARIN: ARDC's POC -- tell ARDC to expect and click it) |
| Prefix verification + activation | **24-48 h** after confirmation | the instance's **BGP** tab in the console shows credentials + example configs; `GET http://169.254.169.254/v1.json` on the instance gains `bgp.ipv4.*` / `bgp.ipv6.*` keys |
| Transit filter propagation | 6-24 h in ewr/ams (dynamic); 48 h+ in manually filtered regions (India, Israel, parts of LatAm/Asia) | RIS visibility climbs after the first announcement |

Nothing is announced until BIRD says so: the generated `state.conf` starts
with `ANNOUNCE = false`.

## 3. After approval

1. Confirm the peer parameters on the instance (they must match `address-plan.yaml` `bgp.upstreams.vultr`):

   ```
   curl -s -H 'Metadata-Token: cloudinit' http://169.254.169.254/v1.json | jq .bgp
   # expect: ipv4.peer-address 169.254.169.254, ipv4.peer-asn 64515,
   #         ipv6.peer-address 2001:19f0:ffff::1, my-asn 215520
   ```

2. Deploy (`docs/runbooks/network-bootstrap.md` step 7): `scripts/netgen --overrides <tofu outputs>`, `scripts/deploy ewr1`, then on the node `bgp-announce status` (withdrawn) and `bgp-announce announce` when health is green.

3. Session verification on the node:

   ```
   sudo birdc show protocols                 # vultr4 / vultr6: "Established"
   sudo birdc show protocols all vultr4      # Routes: 1 imported (the default), 1 exported
   sudo birdc show route export vultr4       # exactly 44.32.58.0/24
   sudo birdc show route export vultr6       # exactly 2a0f:85c1:368::/48
   sudo birdc show route where net = 0.0.0.0/0 all   # learned default, not installed in kernel
   ip route get 44.32.58.1; ip -6 route get 2a0f:85c1:368:1::1   # local (dummy0), never via provider default
   ```

4. Global verification (allow 15-60 min for RIS/looking glasses):

   - `scripts/netcheck --expect announced` (rows `routing:*` OK, `routing:AS215520-prefixes` lists both)
   - RIPEstat looking glass: https://stat.ripe.net/data/looking-glass/data.json?resource=44.32.58.0/24 and https://stat.ripe.net/data/looking-glass/data.json?resource=2a0f:85c1:368::/48 -- paths must end `20473 215520`
   - RIPEstat routing status: https://stat.ripe.net/data/routing-status/data.json?resource=44.32.58.0/24 (`ris_peers_seeing` rising toward `total_ris_peers`)
   - RPKI from RIS' point of view: https://stat.ripe.net/data/rpki-validation/data.json?resource=AS215520&prefix=44.32.58.0/24
   - bgp.tools: https://bgp.tools/prefix/44.32.58.0/24 , https://bgp.tools/prefix/2a0f:85c1:368::/48 , https://bgp.tools/as/215520 (upstreams should list AS20473; the "IPv6 only" tag should disappear)
   - Vultr's own view: instance > BGP tab shows received prefixes; a support ticket answers "is my prefix accepted by the edge" if in doubt
   - Reachability from outside: `curl -sv --max-time 5 gemini://git.as215520.net/` is not curl-able; use `openssl s_client -connect 44.32.58.1:1965` from a host outside Vultr, and `ssh -p 22 -o BatchMode=yes git@44.32.58.1` for the banner

5. Record in the operator's notes: ticket number, approval date, which prefixes/locations were enabled, LOA expiry.

## 4. Changes later

| Change | How |
| --- | --- |
| Add a region | nothing on Vultr's side once BGP is enabled account-wide; deploy the instance, the session comes up with the same password |
| Add/remove a prefix | console *BGP > prefixes* (add) or support ticket (remove); max 5 |
| Rotate the MD5 password | console BGP settings + `scripts/secrets rotate bgp` (expect a session bounce; drain first) |
| Vultr removes a prefix | happens automatically if the ROA becomes invalid or ownership changes -- `scripts/netcheck` rows `rpki:*` are the early warning |
