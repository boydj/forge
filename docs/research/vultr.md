# Vultr research: anycast with a customer ASN and BYOIP via OpenTofu

Researched 2026-09-10. Target: announce a RIPE-issued IPv4 /24 and IPv6 /48 from AS215520 out of
several Vultr regions (anycast), provisioned with OpenTofu and configured with cloud-init + BIRD.

Sourcing note: `docs.vultr.com` pages fetch fine; `www.vultr.com` (features, API reference, pricing) and
`blogs.vultr.com` return HTTP 403 to non-browser clients, so API-reference facts below are corroborated via
the public `api.vultr.com/v2` endpoints, Vultr's own SDKs/CLI/provider source on GitHub, and search
snippets of the API reference. Items marked **VERIFY** could not be confirmed against a primary source.

---

## 1. BGP on Vultr

**Enablement is a human-reviewed request, not an API call.**
Console path: *Products > Network > BGP > Get Started* (`https://console.vultr.com/bgp/setup/`).
Prerequisite: at least one active Compute instance on the account.
Form fields (own-IP-space path): toggle "I have my own IP space", ASN, BGP password (customer-defined,
used for TCP-MD5), IP block(s), **LOA upload**, route preference (No Routes / Default Only / Full Table;
full table not supported on Bare Metal), and a short use-case description. A Vultr representative reviews
and enables BGP.
Source: https://docs.vultr.com/products/network/bgp/request-access

Alternative path (no own ASN): Vultr assigns private **AS64515** and you can only announce Vultr
*Reserved IPs*, which are region-bound and never re-originated to the internet. Not our case.
Source: https://docs.vultr.com/products/network/bgp/asn-information/as64515

**Peer parameters (Cloud Compute / VPS):**

| Item | Value |
|---|---|
| Vultr peer ASN seen from a VPS | 64515 (Vultr's internal ASN on the hypervisor side) |
| IPv4 neighbor | 169.254.169.254 |
| IPv6 neighbor | 2001:19f0:ffff::1 |
| Multihop | 2 (required) |
| Auth | TCP MD5, password chosen by you in the BGP form |
| Session source | the instance's **main** IPv4 / main IPv6 only; secondary IPs are rejected |
| Minimum announce | /24 IPv4, /48 IPv6 |

Bare Metal differs: peer ASN 20473, IPv4 neighbor 169.254.1.1, same IPv6 neighbor, multihop 2.
Source: https://docs.vultr.com/configuring-bgp-on-vultr

Despite peering with 64515 on a VPS, prefixes from an account configured with a public ASN are
re-exported by AS20473 to its transits/peers with the customer ASN preserved, i.e. the world sees
`20473 215520`. Vultr states "Vultr will announce your prefix(es) to our upstream providers" in the
deployment locations you choose, and the AS20473 page describes the external path as `20473_<customer>`.
Sources: https://docs.vultr.com/configuring-bgp-on-vultr ,
https://docs.vultr.com/products/network/bgp/asn-information/as20473 , field report
https://vojk.au/posts/bgp_with_vultr/ (public ASN 44354 peering with 64515 on a VPS, globally reachable).

Per-instance credentials and example BIRD/FRR configs are shown on each instance's **BGP** tab in the
console once enabled. Source: https://docs.vultr.com/products/network/bgp/access-credentials

**IPv6 caveat from the field:** BIRD needs a route to 2001:19f0:ffff::1/128; with RA-derived default via
`fe80::1` this normally exists, but at least one report needed an explicit `route 2001:19f0:ffff::1/128 via
fe80::...%eth0`. Use the exact Vultr-assigned IPv6 as the session source. **VERIFY on first deploy.**
Source: https://vojk.au/posts/bgp_with_vultr/

### AS20473 communities (customer guide)

Source: https://docs.vultr.com/products/network/bgp/asn-information/as20473-bgp-communities-customer-guide
(FAQ pointer: https://docs.vultr.com/support/products/network/what-bgp-communities-does-vultr-support)

**Default behaviour:** with no action community, Vultr advertises the prefix to all transits, IX and private
peers in the regions where it is learned. There is no implicit no-export. Note that **20473:6000 means "do
NOT advertise globally"** (the opposite of a common assumption); leave it off for anycast.

Global action communities (standard):

| Community | Effect |
|---|---|
| 20473:6000 | Do not advertise to anyone outside AS20473 |
| 20473:6001 | Prepend 20473 once to all |
| 20473:6002 | Prepend twice to all |
| 20473:6003 | Prepend three times to all |
| 20473:64609 | Set MED 0 to all |
| 20473:6601 | Do not announce to IXP peers |
| 20473:6602 | Override do-not-advertise for IX route servers only |
| 20473:6603 | Override do-not-advertise for IX route servers and bilateral peers |
| 20473:666 | Blackhole (export to all peers) |

Per-peer action communities (replace `peer-as`; large-community form in parentheses):
`64600:peer-as` do not advertise (`20473:6000:peer-as`), `64601:peer-as` prepend 1x (`20473:6001:peer-as`),
`64602:peer-as` prepend 2x (`20473:6002:peer-as`), `64603:peer-as` prepend 3x (`20473:6003:peer-as`),
`64609:peer-as` MED 0 (`20473:6009:peer-as`), `64699:peer-as` override do-not-advertise (`20473:6099:peer-as`).

Informational communities on routes you receive: `20473:100` transit (`20473:100:transit-as`), `20473:200`
public peer (`20473:200:ixp-as`), `20473:300` private peer, `20473:500` AS20473-originated,
`20473:4000` customer-originated, `20473:<2-digit POP>` location (e.g. 20473:11 Piscataway NJ), and the
large location community `20473:0:3RRRCCC1PP` (UN M49 region/country + POP code).
Customer-defined communities are passed through transparently.

---

## 2. BYOIP

Policy: https://docs.vultr.com/platform/customer-advisory/byoip
Onboarding timeline: https://docs.vultr.com/vultr-ip-prefix-onboarding-process-and-timeline
RPKI: https://docs.vultr.com/products/network/bgp/rpki
Add prefixes (console): https://docs.vultr.com/products/network/bgp/add-prefixes
FAQ: https://docs.vultr.com/support/products/network/can-i-use-my-own-ip-space-for-vultr-instances

* Prefix sizes: IPv4 between /16 and **/24**; IPv6 between /32 and **/48**. IPv6 BYOIP is supported.
* Up to 5 prefixes per account (v4 + v6 combined). Prefix must be RIR-registered; cannot be shared
  between accounts.
* **BGP from the instance is mandatory.** Vultr does not statically route customer space to instances and
  does not offer a "Vultr-originated BYOIP" mode for customer ASNs; the only Vultr-originated option is the
  AS64515 / Reserved-IP path for Vultr-owned space. (No "BGP secondary" product was found in docs, API, SDKs,
  CLI or provider.)
* Anycast is explicitly allowed: "Can be announced from single or multiple locations".
* **LOA required** for own IP space. Wording must authorize "Vultr with AS20473 to announce the following IP
  address blocks" (per IPXO's Vultr guide quoting Vultr; the request-access doc requires the upload).
  Source: https://www.ipxo.com/kb/technical-guides/how-to-prepare-your-leased-subnet-for-vultr-byoip/
* **ROA:** "recommended" to speed acceptance, but Vultr runs nightly RPKI checks and flags
  "Invalid ASN: none of the ASNs match what your account is configured for". With the account configured
  for AS215520 the ROA must authorize **AS215520** as origin (maxLength 24 / 48 to prevent more-specific
  hijack; Vultr only needs the exact prefix). Only accounts using Vultr's private ASN sign for AS20473.
  Multiple ROAs for the same prefix are fine. Recommendation: ROA for AS215520 only.
* IRR: route/route6 object in RADb, ARIN, RIPE or APNIC; Vultr auto-creates one in RADb if missing and adds
  the customer ASN to `RADB::AS20473:AS-CONE`. Create RIPE `route`/`route6` objects for AS215520 beforehand.
* Timeline: request checked immediately (Spamhaus, ROA); confirmation email to the RIR POC the same day
  (must click); 24-48 h further verification then activation; transit filters 6-24 h where dynamic
  (US, CA, EU, JP, ZA), **48 h+ where manual (India, Israel, local transits in Asia/LatAm)**.
* After approval: add subnet configuration in console, announce from instances, wait for filters.
* Removal: on request, or automatically if ROA becomes invalid or ownership changes.
* Reserved IP vs BYOIP: Reserved IPs are Vultr space, region-bound, attachable to one instance, announced
  by Vultr (optionally steered by your AS64515 session). BYOIP is your space, global, announced only by you.

**API (from the API reference, blocked to fetch; per search snippets and SDK absence):**
`GET /v2/account/bgp/prefixes` lists BGP prefixes on the account;
`POST /v2/account/bgp/setup` submits the BGP request (prefixes[], asn, password, letter of authorization,
requested routes, usecase) and returns a support-ticket reference. Neither endpoint exists in govultr,
vultr-cli 3.11, the Terraform provider, or the Ansible collection, so these remain manual/console steps.
No `GET /v2/account/bgp` endpoint was found. **VERIFY** field names in the console/API reference.

---

## 3. Anycast on Vultr

* FAQ confirms the model: deploy instances in multiple regions, run a BGP session from each, announce the
  same prefix; Vultr routes inbound to the closest instance. Within one region multiple announcers are
  ECMP-balanced. Source: https://docs.vultr.com/support/products/network/can-i-implement-anycast-with-vultr
* Marketing statement: BGP announcement available in any Vultr cloud location.
  Source: https://blogs.vultr.com/Announce-IP-Space-on-the-Cloud-with-Vultr (403 to fetch; from snippet)
* No default no-export; do **not** attach 20473:6000. Use per-region prepends (20473:6001..6003) or
  per-peer `6460x:peer-as` if a region attracts too much traffic.
* Region choice caveat: regions with manually filtered transit (blr/bom/del, tlv, parts of LatAm/Asia)
  take longer to become reachable and are worse first-wave picks.
* Route each announcer's BIRD `router id` from its own main IPv4; bind service addresses to a dummy
  interface (Vultr's HA guide pattern:
  `ip link add dev ha-ip type dummy; ip addr add dev ha-ip 192.0.2.4/32; ip addr add ... /64`).
  Source: https://docs.vultr.com/how-to-set-up-high-availability-using-vultr-reserved-ip-and-bgp
* Community walkthroughs: https://labs.ripe.net/author/samir_jafferali/build-your-own-anycast-network-in-nine-steps/

---

## 4. IPv6

* Each instance gets one /64 (`v6_network`/`v6_network_size` in the provider; `interfaces[].ipv6.network`
  + `prefix` in metadata). Address is SLAAC/RA on public NIC; gateway is `fe80::1`.
  Sources: https://docs.vultr.com/configuring-ipv6-on-your-vps ,
  https://docs.vultr.com/products/compute/cloud-compute/networking/ipv6 (404 at fetch time; from snippet),
  cloud-init datasource (accept-ra + ipv6_slaac): https://github.com/canonical/cloud-init/blob/main/cloudinit/sources/helpers/vultr.py
* Additional /64s can be assigned ("Assign IPv6 Network") and reserved IPv6 subnets attached, but an
  instance must keep a non-reserved /64; API `GET /v2/instances/{id}/ipv6` (govultr `ListIPv6`).
* IPv6 BGP works (peer 2001:19f0:ffff::1, ASN 64515 on VPS). Session source must be the main IPv6.
* Cloud-init nameservers Vultr injects: 108.61.10.10 and 2001:19f0:300:1704::6.
* IPv6-only plans exist (`vc2-1c-0.5gb-v6`, $2.50) but cannot carry the IPv4 session.

---

## 5. Instances, regions, pricing (from `api.vultr.com/v2/plans|regions|os`, 2026-09-10)

Cheapest plans (monthly USD, bandwidth GB):

| Plan | vCPU/RAM/disk | BW | $/mo | Notes |
|---|---|---|---|---|
| vc2-1c-0.5gb-v6 | 1/512M/10G | 512 | 2.50 | IPv6-only sandbox, 2 per account, only in: ewr,atl |
| vc2-1c-0.5gb | 1/512M/10G | 512 | 3.50 | sandbox, 5 per account, only in: ewr |
| **vc2-1c-1gb** | 1/1G/25G SSD | 1024 | 5.00 | 31 regions (sao 7.50) |
| vhf-1c-1gb | 1/1G/32G NVMe | 1024 | 6.00 | high-frequency |
| **vhp-1c-1gb-intel** / -amd | 1/1G/25G NVMe | 2048 | 6.00 | 32 / 31 regions, 2 TB egress |
| vc2-1c-2gb | 1/2G/55G | 2048 | 10.00 | |

BGP is tied to the account, not the plan; docs and the features page say it is available on all Cloud
Compute locations and on Bare Metal. Sandbox-plan (512 MB) BGP is not documented either way (**VERIFY**);
`vc2-1c-1gb` or `vhp-1c-1gb-intel` are the safe floor (BIRD is tiny; 1 GB is plenty).

Regions (33): ams atl blr bom cdg del dfw ewr fra hnl icn itm jnb lax lhr mad man mel mex mia mxp nrt ord
sao scl sea sgp sjc sto syd tlv waw yto. `hnl` lacks vc2 but has vhp; `mxp` has no vc2/vhp 1 GB plan.
All regions expose `ddos_protection`; `connectivity: public_ip, nat_gateway`.

Add-on pricing: bandwidth overage $0.01/GB; automatic backups +20% of plan; snapshots $0.05/GB-month
(https://blogs.vultr.com/Vultr-will-begin-charging-for-snapshot-storage-on-October-1/); block storage
HDD $25/TB-month ($0.025/GB, storage_opt) and NVMe $100/TB-month ($0.10/GB, high_perf)
(https://discover.vultr.com/block-storage-datasheet); DDoS protection ~$12/mo per instance (third-party
pricing pages; **VERIFY**). Hourly billing; `hourly_cost` 0.007 for vc2-1c-1gb.

OS image IDs: Debian 12 = **2136**, Debian 13 = **2625**, Ubuntu 22.04 = 1743, Ubuntu 24.04 = **2284**,
Ubuntu 26.04 = 2760. (Also Alpine 2076, Rocky 10 2594, FreeBSD 15 2720, OpenBSD 7.9 2772.)

---

## 6. Firewall

* `vultr_firewall_group` + `vultr_firewall_rule` (`firewall_group_id, protocol icmp|tcp|udp|gre|esp|ah,
  ip_type v4|v6, subnet, subnet_size, port "22" or "8000:9000", source "" | "cloudflare", notes`).
  Source: https://github.com/vultr/terraform-provider-vultr/blob/master/website/docs/r/firewall_rule.html.markdown
* Vultr's firewall filters **inbound** traffic only, for IPv4 and IPv6, attached per instance
  (https://docs.vultr.com/products/network/firewall-groups/management/rules). There is no documentation that
  it covers traffic to BYOIP addresses routed to the instance, and it cannot express stateful/egress/rate
  policy. **Host nftables is still required**: allow TCP/179 from 169.254.169.254/32 and
  2001:19f0:ffff::1/128, ICMPv6 ND/RA, WireGuard, and the service ports on the anycast addresses.
* The metadata/BGP peer is link-local and reached over the main NIC; keep 169.254.169.254 reachable
  (do not drop link-local in nftables output chain).

## 7. VPC

* `vultr_vpc` (region, description, v4_subnet, v4_subnet_mask) is the supported product; VPC is
  **region-bound, IPv4 only**, up to 5 per location, no peering. VPC 2.0 (`vultr_vpc2`) was deprecated in
  provider v2.25.0 (2025-03) though the resource still ships in 2.32.0.
  Sources: https://docs.vultr.com/products/network/vpc-networks/faq ,
  https://docs.vultr.com/products/network/vpc-2 , provider CHANGELOG.
* Cross-region private connectivity is not offered; use WireGuard over public IPv6 between nodes.

## 8. OpenTofu / Terraform provider

* `vultr/vultr` **v2.32.0** (2026-07-14), source registry.terraform.io mirrored by registry.opentofu.org.
  Verified locally: `tofu init` with `version = "~> 2.32"` resolves 2.32.0 and `tofu providers schema`
  lists 52 resources / 42 data sources. Provider args: `api_key` (or env `VULTR_API_KEY`),
  `rate_limit` (ms, default 500), `retry_limit` (default 3).
  Sources: https://registry.terraform.io/providers/vultr/vultr/latest/docs ,
  https://github.com/vultr/terraform-provider-vultr/releases
* `vultr_instance` attributes (from schema): activation_email, app_id, app_variables, backups,
  ddos_protection, disable_public_ipv4, enable_ipv6, firewall_group_id, hostname, image_id, ipxe_chain_url,
  iso_id, label, os_id, plan, region, reserved_ip_id, script_id, snapshot_id, ssh_key_ids, tags, user_data,
  user_scheme, vpc_ids, vpc2_ids, vpc_only; exported main_ip, v6_main_ip, v6_network, v6_network_size,
  internal_ip, gateway_v4, netmask_v4, allowed_bandwidth, default_password, features, status, power_status.
  `hostname` change forces reinstall. `user_data` is plain text (provider base64-encodes for the API).
* Other relevant: vultr_ssh_key, vultr_startup_script, vultr_reserved_ip (region, ip_type v4|v6, label,
  instance_id), vultr_firewall_group/rule, vultr_vpc, vultr_instance_ipv4, vultr_reverse_ipv4/ipv6,
  vultr_snapshot, vultr_block_storage, vultr_dns_domain/record. Data sources: vultr_account, vultr_os,
  vultr_plan, vultr_region, vultr_instance(s), vultr_reserved_ip, vultr_ssh_key, vultr_firewall_group.
* **No BGP or BYOIP resources/data sources exist** (grep of provider tree: none). BGP enablement, prefix
  add and LOA are console/ticket work (`POST /v2/account/bgp/setup`, `GET /v2/account/bgp/prefixes` only).
* Ansible: `vultr.cloud` collection (modules instance, firewall_group/rule, reserved_ip, vpc, ssh_key,
  startup_script ...), also no BGP. vultr-cli 3.11.0 likewise.

## 9. Cloud-init and metadata

* All Vultr Linux images ship cloud-init with the Vultr datasource (BSD/Windows use imageboot scripts).
  API wants base64 `user_data`; console/provider take plain text.
  Source: https://docs.vultr.com/how-to-deploy-a-vultr-server-with-cloudinit-userdata
* Metadata: `http://169.254.169.254/v1.json` (cloud-init sends header `Metadata-Token: cloudinit`).
  Keys (from cloud-init's Vultr datasource tests):
  `bgp.ipv4.{my-address,my-asn,peer-address,peer-asn}`, `bgp.ipv6.{my-address,my-asn,peer-address,peer-asn}`,
  `hostname`, `instance-v2-id` (UUID), `instanceid` (legacy numeric), `interfaces[]{mac, network-type,
  ipv4{address,gateway,netmask,additional[]}, ipv6{address,network,prefix,additional[]}}`,
  `public-keys[]`, `region{regioncode,countrycode}`, `user-defined[]`, `startup-script`, `user-data`,
  `vendor-data`. Individual paths also exist (`/v1/hostname`, `/v1/region/regioncode`, ...).
  Sources: https://github.com/canonical/cloud-init/blob/main/tests/unittests/sources/test_vultr.py ,
  https://docs.cloud-init.io/en/latest/reference/datasources/vultr.html , https://www.vultr.com/metadata/
  (403 to fetch). `bgp.*` is populated only after BGP is enabled on the account (**VERIFY** timing).

## 10. Limits and human steps

* API: 30 requests/second per source IP, HTTP 429 beyond; use backoff (provider `rate_limit` handles it).
  Source: https://docs.vultr.com/support/platform/api/what-rate-limits-apply-to-the-vultr-api
* New accounts start with a small instance cap (community reports: 1 VPS; sandbox 512 MB plans capped at
  2 and 5). Raise via *Billing > Account limits > Request Limit Increase* (identity/use-case form).
  Source: https://docs.vultr.com/platform/billing/manage-account-limits
* BGP request: human review after form submission (needs one live instance first). Prefix onboarding
  adds same-day RIR-POC email confirmation + 24-48 h + 6-48 h transit filter propagation.
* LOA: signed letter on letterhead naming the prefixes and authorizing Vultr / AS20473 to announce.
* Prefix removal requires a support ticket.

## 11. Provider diversity candidates (brief, for later)

| Provider | ASN | BGP / BYOIP | Automation | Notes |
|---|---|---|---|---|
| iFog GmbH (CH) | AS34927 | BGP on VPS >=512 MB, own ASN or private ASN; needs RIPE import/export of AS34927, valid ROA + IRR; LOA only in SG | no known TF provider; contact-driven | 13 locations EU/US/APAC, backbone propagates downstream prefixes. https://ifog.ch/en/about/netzwerk/bgp-faq |
| Servperso (BE) | AS34872 | BGP transit for own ASN v4+v6 from EUR 3.68/mo (NL, Dusseldorf), LocIX access | none known | https://www.servperso.net/bgp-vm (403 to fetch; from snippets) |
| BuyVM / FranTech | AS53667 | free BGP sessions (LV, LU; NY pending); anycast needs a slice in all 4 sites | Stallion panel, limited API, no TF | 1 GB $3.50 incl. /48; chronically out of stock. https://wiki.buyvm.net/doku.php/anycast_vps |
| Virtua.Cloud (FR/NL/US) | AS35661 | automated free BGP + managed BYOIP | REST API; TF provider not found | from EUR 5/mo. https://www.virtua.cloud/features/bring-your-own-ip |
| NetActuate | - | commercial anycast platform | API | enterprise pricing. https://netactuate.com/bgp-anycast |
| Hetzner | - | **no customer BGP** | TF provider | rule out |
| Equinix Metal | - | **shut down 2026-06-30**; IPs/BGP released | - | https://docs.equinix.com/metal/eos-faq/ |

Not verified this round: Misaka, Path.net (DDoS transit, not VPS), HostBrr.

## Local environment findings (2026-09-10)

* No `VULTR_API_KEY` / `CLOUDFLARE_API_TOKEN` in env, shell rc files, `~/.config/vultr*`, `~/.vultr*`,
  `~/.config/cloudflare*`, `~/.cloudflare*`, `~/.terraformrc`, or repo `.env`.
* Installed to `~/.local/bin` (no sudo available): OpenTofu 1.12.6 (SHA256SUMS verified), sops 3.13.3
  (checksums.txt verified), age/age-keygen 1.3.2 (no SHA256 file published; only sigsum `.proof`; tarball
  sha256 cbe24006683f8eb669266162894b9a522a1af52f2665fbc63a4bb032ed26ac10), vultr-cli 3.11.0 (checksums
  verified). `terraform` and `ansible` remain absent (ansible installable via `pip --user`/pipx).
