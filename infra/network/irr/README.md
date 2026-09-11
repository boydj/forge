# IRR objects for AS215520

RPSL templates for the objects `docs/network-readiness.md` (items 2, 3, 7, 9)
and `infra/network/address-plan.yaml` (`irr:`) say must exist. Nothing here is
submitted automatically; `scripts/netcheck` reports whether the live database
matches.

## Where each object lives, and why

| Object | Database | Who can write it | Why there |
| --- | --- | --- | --- |
| `aut-num: AS215520` | **RIPE** | operator (`JOSHBOYD-MNT`; `RIPE-NCC-END-MNT` co-maintains the RIR-managed attributes) | The ASN is a RIPE NCC resource. The aut-num is authoritative only in the RIR that issued the ASN; RADB mirrors RIPE, so nothing needs to be copied there. |
| `as-set: AS215520:AS-ALL` | **RIPE** | operator (`JOSHBOYD-MNT`) | Hierarchical name `AS215520:...` can only be created by the maintainer of `aut-num AS215520` (RIPE authorisation rule), which makes it unforgeable. RADB/ALTDB copies are unnecessary: every IRR mirror and `bgpq4 -S RIPE` resolve it. PeeringDB references it as `RIPE::AS215520:AS-ALL`. |
| `route6: 2a0f:85c1:368::/48 origin AS215520` | **RIPE** (exists) | operator, via `mnt-routes: JOSHBOYD-MNT` on the inet6num | The /48 is RIPE address space; RIPE only accepts a route6 authorised by the inet6num's `mnt-routes` (or `mnt-by`), so a RIPE route6 proves both ASN and prefix authorisation. The RADB copy (`MAINT-AS20473`, "Vultr Customer Route") is Vultr's own and needs no action. |
| `route: 44.32.58.0/24 origin AS215520` | **RADB** (exists, `MAINT-ARDC`) | **ARDC** only | The /24 is ARIN space registered to ARDC (`NET-44-0-0-0-1`). ARIN's IRR requires the ARIN Online org that holds the net block, i.e. ARDC, and RIPE refuses `route` objects for non-RIPE space unless they are created as `RIPE-NONAUTH`, which upstreams and IX route servers now ignore. ARDC publishes 44Net route objects in RADB from its portal, so the correct move is to *ask ARDC* (see `ardc-request.md`), never to create a competing ALTDB/RIPE-NONAUTH object. |

Authoritative sources checked (2026-09-10): RIPE DB REST
(`rest.db.ripe.net/ripe/{aut-num,as-set,route6,inet6num}`), RADB
(`whois.radb.net`), ARIN RDAP.

## Submitting from the repository

With a Database API key in the bundle (`scripts/secrets edit
infra/secrets/dev.enc.yaml`, key `ripe_db_api_key: "KEYID:SECRET"`):

```
scripts/ripe-submit diff  infra/network/irr/as-set-AS215520-AS-ALL.rpsl infra/network/irr/aut-num-AS215520.rpsl   # live vs template
scripts/ripe-submit check infra/network/irr/as-set-AS215520-AS-ALL.rpsl infra/network/irr/aut-num-AS215520.rpsl   # REST dry run
scripts/ripe-submit apply infra/network/irr/as-set-AS215520-AS-ALL.rpsl infra/network/irr/aut-num-AS215520.rpsl   # for real
scripts/netcheck                                                                                                # verify
```

Create the as-set before the aut-num update: the aut-num exports reference it.

## How to submit to the RIPE DB (by hand)

`JOSHBOYD-MNT` authenticates with **RIPE NCC Access (SSO) only**; there is no
`MD5-PW` or PGP credential on the maintainer and none should be added. That
rules out plain e-mail/`syncupdates` submissions with a `password:` line, so:

1. Preferred: **Webupdates** at https://apps.db.ripe.net/db-web-ui/ -- log in
   with the RIPE NCC Access account linked to the maintainer, *Create an
   object* (or *Update* for the aut-num), switch to the *text* view and paste
   the template body. Lines starting with `#` are comments and must be removed.
2. Alternative: the REST API with a **Database API key** (RIPE DB > My Account
   > API keys; keys are bound to the SSO user and expire). Then
   `syncupdates` works too:

   ```
   curl -X POST -H "Authorization: Basic $(printf 'apikey:%s' "$RIPE_DB_API_KEY" | base64 -w0)" \
        --data-urlencode "DATA@as-set-AS215520-AS-ALL.rpsl" \
        https://syncupdates.db.ripe.net/
   ```

   Never commit a key; keep it in the operator's password manager, not in SOPS
   (the deploy pipeline has no business writing to the RIPE DB).

Attributes the RIPE NCC manages (`sponsoring-org`, `status`, `mnt-by:
RIPE-NCC-END-MNT`, `created`, `last-modified`, `source`) must be submitted
unchanged; Webupdates enforces this.

## Files

| File | Purpose |
| --- | --- |
| `aut-num-AS215520.rpsl` | full replacement for the aut-num (import/export for AS20473, AS835 kept, stale AS209735/AS207841 dropped, remarks, as-set in exports) |
| `as-set-AS215520-AS-ALL.rpsl` | new object |
| `route6-2a0f-85c1-368--48.rpsl` | current object, for verification only (no change) |
| `route-44.32.58.0-24.rpsl` | the RADB object as ARDC publishes it, for verification; plus the ALTDB/RIPE-NONAUTH non-options |
| `ardc-request.md` | exact wording for the ARDC portal / ticket (route object, ROA, reverse DNS, LOA) |

Verify after each change:

```
scripts/netcheck --expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"
whois -h whois.ripe.net -- '-r -T aut-num AS215520'
whois -h whois.ripe.net -- '-r -T as-set AS215520:AS-ALL'
bgpq4 -6 -S RIPE -l as215520 AS215520:AS-ALL     # should expand to the /48
whois -h whois.radb.net -- '-i origin AS215520'  # /24 and /48, mirrored
```
