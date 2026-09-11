# Runbook: network bootstrap, from today's state to a routed first POP

Status: 2026-09-10. Starting point is `docs/network-readiness.md` (public
facts) and the drift table from `scripts/netcheck` (below). Target: `ewr1`
announces `44.32.58.0/24` and `2a0f:85c1:368::/48` from AS215520 via Vultr
AS20473, `git.as215520.net` resolves to the anycast addresses, and
`scripts/netcheck --expect announced` exits 0.

Nothing in this runbook is automated end to end: steps 1-4 and 6 are human
work at registries and providers; steps 5 and 7 are `tofu` and `scripts/*`.
Steps with third parties (2, 6, 9) have the longest lead time, so start them
first; the ordering below is the dependency order, not the calendar order.

Actors: **operator** (Josh, RIPE SSO `JOSHBOYD-MNT`, Vultr/Cloudflare/
Namecheap/PeeringDB accounts), **ARDC** (44Net portal, ARIN hosted RPKI),
**Inferno** (sponsoring LIR, `INFERNO-MNT`), **Vultr** (BGP review team).

## State today

`scripts/netcheck` at 2026-09-10T11:34Z (default expectation: both prefixes announced):

```
CHECK                                    STATUS  EXPECTED                                                    ACTUAL                                                        NOTE
yaml:consistency                         OK      plan and desired-roas agree                                 2 prefixes, 2 ROAs, AS215520
rpki:2a0f:85c1:368::/48                  OK      valid (AS215520 maxLength 48)                               valid (ROA 2a0f:85c1:368::/48 maxLength 48)
rpki:44.32.58.0/24                       DRIFT   valid (AS215520 maxLength 24)                               unknown                                                       ROA missing or wrong; see desired-roas.yaml (arin-hosted-via-ardc)
routing:44.32.58.0/24                    DRIFT   announced by AS215520                                       withdrawn: 1/326 RIS peers                                    1 stale RIS path(s)
routing:2a0f:85c1:368::/48               OK      announced by AS215520                                       announced: 320/320 RIS peers, origin AS215520
routing:AS215520-prefixes                OK      only 44.32.58.0/24, 2a0f:85c1:368::/48                      2a0f:85c1:368::/48                                            RIS window ending 2026-09-10T08:00:00
ripe:aut-num                             DRIFT   import/export for AS835, AS20473                            peers: AS207841, AS209735                                     missing AS835, AS20473; not in plan: AS207841, AS209735
ripe:as-set                              DRIFT   AS215520:AS-ALL members AS215520 mnt-by JOSHBOYD-MNT        object missing                                                create it (infra/network/irr/as-set-AS215520-AS-ALL.rpsl)
ripe:route6:2a0f:85c1:368::/48           OK      origin AS215520 mnt-by JOSHBOYD-MNT                         origin AS215520 mnt-by JOSHBOYD-MNT,JOSHBOYD-SHARED-MNT
ripe:inet6num:2a0f:85c1:368::/48         OK      mnt-routes JOSHBOYD-MNT                                     mnt-routes JOSHBOYD-MNT; mnt-by INFERNO-MNT; mnt-domains none
radb:route:44.32.58.0/24                 OK      origin AS215520 mnt-by MAINT-ARDC                           origin AS215520 mnt-by MAINT-ARDC descr KN4LJL
peeringdb:net                            DRIFT   irr_as_set RIPE::AS215520:AS-ALL, prefixes4 1, prefixes6 1  net 35354: irr_as_set (empty), prefixes4 100, prefixes6 100   irr_as_set=(empty); info_prefixes4=100; info_prefixes6=100
peeringdb:pocs                           DRIFT   public POCs: Abuse, Technical, Policy                       0 visible: none                                               missing or not public: Abuse, Technical, Policy
dns:as215520.net:NS                      OK      delegated to *.ns.cloudflare.com                            fay.ns.cloudflare.com, kip.ns.cloudflare.com
dns:as215520.net:DS                      DRIFT   DS present at the .net parent                               none                                                          publish the DS from `tofu output dnssec_ds` at Namecheap
dns:git.as215520.net:A                   DRIFT   44.32.58.1                                                  no record (rcode 0)                                           apply infra/network/generated/dns-nodes.yaml via infra/opentofu
dns:git.as215520.net:AAAA                DRIFT   2a0f:85c1:368:1::1                                          no record (rcode 0)                                           apply infra/network/generated/dns-nodes.yaml via infra/opentofu
dns:58.32.44.in-addr.arpa:NS             DRIFT   NS = fay.ns.cloudflare.com, kip.ns.cloudflare.com           not delegated (NXDOMAIN)                                      delegate via ARDC portal
dns:8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa:NS  DRIFT   NS = fay.ns.cloudflare.com, kip.ns.cloudflare.com           not delegated (NXDOMAIN)                                      delegate via Inferno (INFERNO-MNT domain object)

netcheck: 8 ok, 11 drift, 0 unknown, 0 skipped -> exit 1
```

With `--expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"` (what
is true today) the routing rows are OK and the count is 9 ok / 10 drift. Each
step below names the rows it turns green.

Conventions: run from the repository root; `S=` prefixes mean "on the node".
Every step ends with a verification and a rollback. Do not skip the
verification; the next step assumes it.

---

## Step 0. Decide the /48 question (operator; blocks step 7 for IPv6)

`2a0f:85c1:368::/48` is announced today with 100 % visibility via AS835
(GoCodeIT / Xenyth, Toronto). Announcing it from `ewr1` as well makes Toronto
an anycast site that does not run the forge (`docs/network-architecture.md`
section 6). Options, restated:

| Option | What happens | When to pick |
| --- | --- | --- |
| (a) keep Toronto announcing, add POPs | Toronto's catchment black-holes `2a0f:85c1:368:1::1` unless the forge also runs there | only if Xenyth becomes a real POP (`yto1`, joins the mesh) |
| (b) move the /48 to the forge POPs | withdraw at AS835 once ewr1 is healthy; anything served from Toronto on the /48 moves first | **recommended**; readiness found nothing on the /48 in Toronto (`gemini.as215520.net` points at Vultr addresses) |
| (c) new /48 for the forge (PI, or a second PA /48) | Toronto untouched; costs LIR fee + weeks; also removes the PA-tied-to-Inferno risk | strategic follow-up to (b), not a prerequisite |

**Recommendation: (b).** Inputs: an inventory of what the Toronto box serves
on `2a0f:85c1:368::/48` (log in to it; `ip -6 addr`, listening sockets, DNS
records pointing into the /48). Decision recorded by the operator in
`docs/decisions/` (one paragraph). Until it is recorded, step 7 announces
**IPv4 only** (`bgp-announce` gates both families together, so in practice:
do not run `announce` before the decision; see step 7 for the staged test).

Verification: decision file exists; if (b), the Toronto inventory says
"nothing depends on the /48". Rollback: none needed; (b) is reversible until
the AS835 withdraw in step 7d.

## Step 1. RIPE DB objects (operator; self-service; 15 minutes)

Inputs: `infra/network/irr/*.rpsl`, RIPE NCC Access login for `JOSHBOYD-MNT`.
Auth is SSO only (no password/PGP on the maintainer), so use Webupdates:
https://apps.db.ripe.net/db-web-ui/ -> log in -> *Create* / *Update* -> text view
-> paste, minus the `#` lines. (`infra/network/irr/README.md` covers the
API-key alternative.)

1. Create `as-set AS215520:AS-ALL` from `as-set-AS215520-AS-ALL.rpsl`.
2. Update `aut-num AS215520` with `aut-num-AS215520.rpsl` (AS20473 v4+v6,
   AS835 v6 kept until step 7d, stale AS209735/AS207841 removed, remarks).
   Keep `sponsoring-org`, `status`, `mnt-by: RIPE-NCC-END-MNT` verbatim.
3. Nothing to do for the route6 (verify only, `route6-2a0f-85c1-368--48.rpsl`).

Verification (rows `ripe:as-set`, `ripe:aut-num` -> OK):

```
scripts/netcheck --expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"
whois -h whois.ripe.net -- '-r -T as-set AS215520:AS-ALL'
```

Rollback: Webupdates keeps object history; *Update* with the previous version
(shown in `docs/network-readiness.md` section 1) or *Delete* the as-set.
Neither affects routing.

## Step 2. ARDC actions for the /24 (operator asks; ARDC executes; days to weeks)

Inputs: `infra/network/irr/ardc-request.md` (the exact ticket text),
`infra/network/vultr/loa-template.md` (LOA draft to attach), portal login at
https://portal.ampr.org.

Action: check the portal first (BGP/DNS tabs of the `44.32.58.0/24`
allocation); then submit the four-item request (route object unchanged, ROA
`44.32.58.0/24` maxLength 24 origin AS215520, NS delegation of
`58.32.44.in-addr.arpa` to `fay/kip.ns.cloudflare.com`, LOA naming AS215520
and Vultr AS20473). Tell ARDC that Vultr will e-mail ARIN's POC for the block
during step 6.

Prerequisite for item 3: the reverse zone must exist in Cloudflare (step 4,
`58.32.44.in-addr.arpa` zone, PTR `1 -> git.as215520.net`) so the delegation
resolves the moment ARDC enters it. Do step 4 before ARDC gets to item 3, or
say in the ticket that the zone will be there by date X.

Verification (rows `rpki:44.32.58.0/24`, `dns:58.32.44.in-addr.arpa:NS` -> OK;
`radb:route:44.32.58.0/24` stays OK; LOA PDF in hand):

```
scripts/netcheck --expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"
dig NS 58.32.44.in-addr.arpa @ns.ardc.net
```

Rollback: the ROA can be deleted by ARDC on request (it would only matter if
the origin changes); an NS delegation is removed the same way. Nothing here
changes what is announced.

## Step 3. PeeringDB (operator; self-service; after step 1; 20 minutes)

Inputs: `infra/network/peeringdb/desired.yaml`, PeeringDB login with admin
on org 37423. Action: https://www.peeringdb.com/net/35354 -> *Edit*:
`irr_as_set` = `RIPE::AS215520:AS-ALL`, prefixes 1/1, policy/notes/website
as in the file; *Contacts*: Abuse, Technical, Policy, visibility Public;
org 37423 address fields. Keep the three IPv6 IX entries.

Verification (rows `peeringdb:net`, `peeringdb:pocs` -> OK; anonymous API is
throttled, so either wait a minute between runs or export a read-only
`PEERINGDB_API_KEY`):

```
PEERINGDB_API_KEY=... scripts/netcheck --expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"
```

Rollback: edit back; PeeringDB has full history per object.

## Step 4. Cloudflare DNS and DNSSEC (operator; `infra/opentofu`; 30 minutes)

Inputs: `CLOUDFLARE_API_TOKEN` in `infra/secrets/dev.enc.yaml`,
`infra/network/generated/dns-nodes.yaml` (from `scripts/netgen`), Namecheap
login.

```
scripts/netgen --check && scripts/netgen                        # refresh dns-nodes.yaml
cd infra/opentofu/environments/dev
cp -n dev.auto.tfvars.example dev.auto.tfvars                   # fill anycast_v4/v6, nodes from dns-nodes.yaml, create_dns=true, create_node=false
eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
tofu init && tofu plan                                          # expect: git.<zone> A/AAAA, SSHFP, nodes records; nothing destroyed
tofu apply
tofu apply -var manage_dnssec=true && tofu output dnssec_ds     # DS record text
```

Then at Namecheap: Domain List -> `as215520.net` -> *Advanced DNS* ->
*DNSSEC* -> add the DS (key tag, algorithm 13, digest type 2, digest) from
`tofu output dnssec_ds`. Also create the two reverse zones in Cloudflare
(`58.32.44.in-addr.arpa`, `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa`; free plan
allows arbitrary zone names) with PTRs `1 -> git.as215520.net` and
`1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.1.0.0.0 -> git.as215520.net`; these can be
added as `extra_records`/a second module call later, by hand is fine now.
Note `git.as215520.net` will resolve to addresses that are not yet routed
(IPv4) or routed to Toronto (IPv6) until step 7 -- acceptable for a name
nobody uses yet; if that is a concern, apply `create_dns` only after step 7.

Verification (rows `dns:as215520.net:DS`, `dns:git.as215520.net:A/AAAA` -> OK
after propagation; DS takes up to a day at the .net registry):

```
scripts/netcheck --expect "44.32.58.0/24=withdrawn,2a0f:85c1:368::/48=announced"
dig +dnssec as215520.net SOA @1.1.1.1 | grep -w ad    # AD flag once the DS is live
```

Rollback: `tofu destroy -target module.dns` (or set `create_dns=false` and
apply); remove the DS at Namecheap **before** disabling DNSSEC in Cloudflare,
otherwise the zone goes bogus.

## Step 5. Vultr instance ewr1 (operator; `tofu apply`; 15 minutes)

Inputs: `VULTR_API_KEY` in `infra/secrets/dev.enc.yaml`, an SSH public key
(`bootstrap_ssh_public_key`), plan `vc2-1c-1gb` region `ewr` (already the
module defaults; `infra/providers/vultr/facts.yaml`). Account instance limit
is 1 for new accounts: enough for this step.

```
cd infra/opentofu/environments/dev
# dev.auto.tfvars: create_node = true, bootstrap_ssh_public_key = "ssh-ed25519 ..."
tofu plan && tofu apply
tofu output pop                                                 # main_ip, v6_main_ip, admin ssh line
cd -
scripts/deploy init-node ewr1                                   # collects the node's age recipient
```

Write the outputs into an overrides file (shape:
`infra/network/overrides.example.yaml`; `pops.ewr1.provider_ipv4/6`,
`wireguard.public_keys.ewr1` from `scripts/secrets gen-wg ewr1` or the node)
and regenerate: `scripts/netgen --overrides infra/network/overrides.dev.yaml`.

Verification: `ssh -p 2200 deploy@<main_ip>` works; `tofu output pop` shows
the provider IPv4/IPv6; `ewr1.nodes.as215520.net` resolves to them (DNS
module merges `pop_dns_node`); `scripts/deploy smoke ewr1` passes on the
provider address. Rollback: `tofu destroy -target module.pop` (hourly
billing; nothing else depends on the instance yet).

## Step 6. Vultr BGP request + LOA (operator submits; Vultr reviews; 1-3 days + 24-48 h)

Inputs: `infra/network/vultr/bgp-request-checklist.md` (prerequisites and the
exact form values), signed LOA PDFs (`loa-template.md`; the /24 letter from
step 2), the MD5 password:

```
openssl rand -base64 24 | tr -d '/+=' | cut -c1-24
scripts/secrets edit infra/secrets/dev.enc.yaml        # vultr_bgp_password: "<value>"   (there is no `secrets set`)
```

Action: https://console.vultr.com/bgp/setup/ (*Products > Network > BGP > Get
Started*): own IP space, ASN 215520, that password, both prefixes, LOA
upload, **Default Only**, use-case text from the checklist. Note the ticket
number. Expect the RIR-POC confirmation e-mail the same day (RIPE: the
operator; ARIN: ARDC -- warn them), then 24-48 h until the instance's BGP tab
shows credentials.

Verification: instance page -> *BGP* tab shows peer `169.254.169.254` /
`2001:19f0:ffff::1`, AS64515, your ASN; on the node
`curl -s -H 'Metadata-Token: cloudinit' http://169.254.169.254/v1.json | jq .bgp`
returns `my-asn 215520`. Rollback: reply on the ticket to cancel; nothing is
announced until step 7.

## Step 7. Deploy BIRD and announce (operator; scripts; 1 hour + propagation)

Inputs: overrides file from step 5, SOPS bundle with `vultr_bgp_password`,
generated `infra/bird/generated/ewr1/{bird.conf,state.conf}` (starts
`ANNOUNCE = false`), step 0 decision.

```
scripts/netgen --overrides infra/network/overrides.dev.yaml     # bird.conf, wg0.conf, dns-nodes.yaml, nftables vars
python3 -m unittest discover -s tests/network                    # bird -p on the generated config
scripts/deploy ewr1                                              # binary + configs + node-scoped secrets; restarts services
```

7a. Session up, nothing announced (row `routing:*` unchanged):

```
S= sudo birdc show protocols            # vultr4, vultr6 Established
S= sudo birdc show route export vultr4  # empty: ANNOUNCE=false
S= bgp-announce status                  # withdrawn
```

7a'. The gate. `forge.toml` only carries `health.announcer` when the POP's
`bgp_announce` variable is true (`infra/opentofu/environments/dev/dev.auto.tfvars`);
without it the health controller runs checks but never calls `bgp-announce`.
Going live is therefore: set `bgp_announce = true`, `scripts/deploy ewr1`, and
watch `journalctl -u forge | grep -i announce` on the node. The controller
announces after six green 10 s ticks and a 120 s cooldown. Manual
`bgp-announce announce` (7b) is the equivalent one-off; the controller keeps
whatever it finds if it is not configured.

Phase 1 note: while the announcement is off, `git.<zone>` A/AAAA point at
the node's provider addresses (`anycast_v4/v6` in the dev tfvars). After 7b
succeeds, put the plan's anycast addresses back (`44.32.58.1`,
`2a0f:85c1:368:1::1`) and `tofu apply`; the node already has them on `dummy0`.

7b. Staged test while Toronto still announces the /48 (option (b) test from
architecture section 6): announce, then check from several vantage points.
Toronto-catchment probes (RIPE Atlas, a Xenyth-hosted host, `mtr` from
Canadian eyeballs) will fail on the /48 -- that is the expected signal, not
a fault. The IPv4 /24 has no competing announcement, so IPv4 goes live for
real here.

```
S= bgp-announce announce                # sets ANNOUNCE=true, birdc configure
S= sudo birdc show route export vultr4  # 44.32.58.0/24
S= sudo birdc show route export vultr6  # 2a0f:85c1:368::/48
scripts/netcheck --expect announced     # routing rows: ris_peers rising; give RIS 15-60 min, transit filters 6-24 h
```

Looking glasses: https://stat.ripe.net/data/looking-glass/data.json?resource=44.32.58.0/24
(paths ending `20473 215520`), https://bgp.tools/prefix/44.32.58.0/24,
https://bgp.tools/as/215520 (upstreams gain AS20473). Service:
`openssl s_client -connect 44.32.58.1:1965 </dev/null` and
`ssh -o BatchMode=yes git@44.32.58.1` from a host outside Vultr.

7c. Health worker takes over: after 6 consecutive green ticks and the 120 s
cooldown it keeps the announcement; a red run drains (`20473:6003` +
`65535:0`) then withdraws. Confirm `bgp-announce status` is not flapping over
an hour (`journalctl -u forge | grep bgp-announce`).

7d. Only for option (b): withdraw the /48 at AS835 (Toronto box: stop its
BIRD export or ask Xenyth), then confirm every RIS path ends `20473 215520`:

```
curl -s 'https://stat.ripe.net/data/looking-glass/data.json?resource=2a0f:85c1:368::/48' | jq -r '.data.rrcs[].peers[].as_path' | sort | uniq -c | sort -rn | head
scripts/netcheck --expect announced                              # all routing rows OK, exit 0 once steps 1-4 are done too
```

Then delete the AS835 lines from the aut-num (step 1 template comment).

Rollback: `S= bgp-announce withdraw` (idempotent, seconds; traffic falls back
to Toronto for the /48 and to nothing for the /24, as today). For a graceful
move: `drain`, wait 300 s, `withdraw`. `scripts/deploy rollback ewr1` swaps
the binary back; BIRD config rollback is `git checkout` of the generated
tree + `scripts/deploy ewr1`.

## Step 8. Second POP -> anycast (operator; repeat 5 and 7 for ams1)

Prerequisites: instance limit raised to >= 2 (Vultr *Billing > Account
limits*); the WireGuard public key of ewr1 registered in the overrides file;
step 7c stable for a few days. Run step 5 for `ams1` (a second environment
or the `production` root module), exchange WireGuard keys
(`wireguard.public_keys` for both), `scripts/netgen --overrides ...`,
`scripts/deploy ams1`, `scripts/deploy ewr1` (peer list changed), then step 7a-7c
on ams1. Verification: `wg show` on both nodes shows a recent handshake;
replication over the mesh works (`docs/replication.md`); from Europe,
`traceroute 44.32.58.1` ends in Amsterdam, from the US in New Jersey;
`scripts/netcheck --expect announced` still exits 0. Rollback: `bgp-announce
withdraw` on ams1 only; ewr1 keeps serving.

## Step 9. Reverse DNS (operator asks; ARDC and Inferno execute)

IPv4: item 3 of the ARDC ticket (step 2). IPv6: e-mail `noc@inferno.net.uk`
asking for `domain: 8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` with `nserver:
fay.ns.cloudflare.com` / `kip.ns.cloudflare.com` and `mnt-by: INFERNO-MNT,
JOSHBOYD-MNT` (or `mnt-domains: JOSHBOYD-MNT` on the inet6num so the operator
can create it). The Cloudflare zones from step 4 must exist first.
Verification (rows `dns:58.32.44.in-addr.arpa:NS`,
`dns:8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa:NS` -> OK):

```
dig -x 44.32.58.1 @1.1.1.1            # git.as215520.net
dig -x 2a0f:85c1:368:1::1 @1.1.1.1    # git.as215520.net
scripts/netcheck --expect announced   # exit 0 = M6 network readiness complete
```

Rollback: ask the same party to remove the delegation; PTRs are cosmetic for
reachability.

## Done criteria

`scripts/netcheck --expect announced` exits 0 (every row OK, no UNKNOWN),
`docs/status.md` updated, LOA/ticket references recorded outside the repo.
Re-run `scripts/netcheck` weekly (or from CI with `--json`); a ROA that turns
invalid or a prefix that vanishes from RIS shows up there before users notice.
