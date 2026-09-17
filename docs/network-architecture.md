# Network architecture: AS215520 anycast for the forge

Status: design, 2026-09-10. Source of truth for addresses and policy is
`infra/network/address-plan.yaml`; everything under `infra/*/generated/` is
produced from it by `scripts/netgen`. Public facts (what the registries and
the routing table say today) are in `docs/network-readiness.md`.

Decisions this document depends on: `docs/decisions/0010-bird-for-bgp.md`
(BIRD 2), `docs/decisions/0011-single-writer-per-repository.md` (leader per
repository, replicas pull over the mesh), `docs/threat-model.md` T-30..T-36.

## 1. Address plan

### 1.1 IPv6 `2a0f:85c1:368::/48`

The /48 is announced whole from every POP. The ROA has maxLength 48, so a
more-specific can never be RPKI-valid; there is no per-POP announcement.

| Prefix | Purpose |
| --- | --- |
| `2a0f:85c1:368::/56` | anycast region |
| `2a0f:85c1:368::/64` | reserved (subnet zero; `::` is never a service address) |
| `2a0f:85c1:368:1::/64` | anycast service addresses |
| `2a0f:85c1:368:1::1` | **the** service address: Gemini + Titan (1965), SSH (22) |
| `2a0f:85c1:368:1::2` | reserved for a second service |
| `2a0f:85c1:368:2::/64` .. `:ff::/64` | future anycast services |
| `2a0f:85c1:368:100::/56` | per-POP unicast region |
| `2a0f:85c1:368:1nn::/64` | POP with index `nn` (hex 01..ff); `::1` is the node address |
| `2a0f:85c1:368:101::/64` | ewr1 (index 1) |
| `2a0f:85c1:368:102::/64` | ams1 (index 2) |
| `2a0f:85c1:368:1fe::/64` | lab (index 254, simulation only) |
| `2a0f:85c1:368:200::/55` .. `:fe00::/56` | reserved for expansion |
| `2a0f:85c1:368:ff00::/56` | infrastructure region |
| `2a0f:85c1:368:fffe::/64` | management / VPN, reserved |
| `2a0f:85c1:368:ffff::/64` | lab and tests (never announced) |

**One service address for everything.** TOFU pinning of the TLS certificate
and SSH host key (threat model T-35/T-36) wants one stable identity behind one
hostname (`git.as215520.net`), and the ports already separate the protocols.
Separate addresses per protocol would only allow per-protocol traffic
engineering, which BGP communities already provide per POP.

**Unicast inside anycast.** A POP's `/64` is part of the anycast `/48`, so a
packet for `2a0f:85c1:368:101::1` (ewr1) from a client in Europe lands on
ams1. Each POP therefore forwards the other POPs' `/64`s over the WireGuard
mesh: wg-quick installs the routes from `AllowedIPs` (peer mesh `/128` plus
peer unicast `/64`) and `net.ipv6.conf.all.forwarding=1` is set on `wg0`
PostUp. Return traffic leaves the owning POP directly (asymmetric, which is
fine for TCP). This makes `<pop>.nodes.as215520.net` a way to test a specific
POP from anywhere, and it keeps working while that POP is drained or
withdrawn. The cost is that reaching a POP on its /48 address needs the mesh
to be up, so the provider address stays the fallback management path
(`nodes_provider` in `dns-nodes.yaml`).

### 1.2 IPv4 `44.32.58.0/24`

| Address | Purpose |
| --- | --- |
| `44.32.58.0` | network address, unused |
| `44.32.58.1` | **the** service address (1965, 22) |
| `44.32.58.2` .. `.15` | future anycast services |
| `44.32.58.16` .. `.255` | reserved |

A /24 is the smallest prefix the default-free zone carries, so it is announced
whole from every POP and every address in it is anycast by construction. We do
**not** carve per-POP unicast IPv4 out of it: without the same mesh backhaul as
for IPv6, an address in the /24 only ever reaches the POP nearest to the
sender, and we chose not to backhaul IPv4 because the block is tiny, IPv4
management must not depend on the mesh, and every Vultr instance already has a
directly reachable provider IPv4. Per-POP unicast IPv4 is therefore the
provider address, filled in at deploy time from OpenTofu outputs.

### 1.3 WireGuard overlay `fda5:bc65:9bb1::/48`

Inner addresses are ULA (RFC 4193), not from the /48, so they can never be
announced or routed globally by accident. The global ID was generated per
RFC 4193 section 3.2.2 (inputs recorded in the plan). The mesh is
`fda5:bc65:9bb1:1::/64`, node address `::<index>` (ewr1 `::1`, ams1 `::2`,
lab `::fe`). IPv6-only: every node has provider IPv6, and the daemon binds
replication and metrics to the `wg0` address only. No inner IPv4.

## 2. Topology

```
                 clients (Gemini / Titan / SSH)  --> 44.32.58.1, 2a0f:85c1:368:1::1
                                   |
          +------------------------+---------------------------+
          |  BGP: nearest POP      |                           |
     Vultr AS20473 (ewr)      Vultr AS20473 (ams)        (future POP n)
     peer 169.254.169.254     peer 169.254.169.254       ...
          |  AS64515 x2 (v4,v6)    |
   +------+--------------+  +------+--------------+
   | ewr1  index 1       |  | ams1  index 2       |
   | eth0: provider v4/v6|  | eth0: provider v4/v6|
   | dummy0: 44.32.58.1  |  | dummy0: 44.32.58.1  |
   |   2a0f:85c1:368:1::1|  |   2a0f:85c1:368:1::1|
   |   ...:101::1/64     |  |   ...:102::1/64     |
   | bird: static /24,/48|  | bird: static /24,/48|
   |   -> vultr4, vultr6 |  |   -> vultr4, vultr6 |
   | wg0: fda5:..:1::1   |  | wg0: fda5:..:1::2   |
   +------+--------------+  +------+--------------+
          |   WireGuard full mesh, UDP 51820 over provider IPv6      |
          +============================================================+
             AllowedIPs: peer /128 + peer unicast /64 (backhaul)
             carries: git fetch replication, forwarded writes, metrics
```

Each POP is one Vultr Cloud Compute instance (`vc2-1c-1gb` or
`vhp-1c-1gb-*`), BGP enabled on the account for AS215520, both prefixes
onboarded as BYOIP. Session source addresses must be the instance's main IPv4
and main IPv6 (Vultr rejects secondaries).

## 3. BGP policy

Generated per POP into `infra/bird/generated/<pop>/bird.conf`, validated with
`bird -p` (BIRD 2.14) in the tests.

| Item | Setting | Why |
| --- | --- | --- |
| Local AS | 215520 | |
| Peer | 169.254.169.254 / 2001:19f0:ffff::1, AS64515, `multihop 2`, TCP MD5 | Vultr cloud-compute parameters (`infra/providers/vultr/facts.yaml`) |
| Router ID | provider IPv4 | unique per POP without touching the /24 |
| Timers | hold 90, keepalive 30, `graceful restart on`, `error wait time 60, 300` | |
| Origination | `protocol static`: `route 44.32.58.0/24 unreachable`, `route 2a0f:85c1:368::/48 unreachable` | aggregates exist independently of interfaces; installed in the kernel as blackholes so unused addresses inside our prefixes never follow the provider default (loop prevention) |
| `protocol direct` | absent | interface addresses can never leak into BGP |
| Kernel | `import none`, `export where source = RTS_STATIC` | never learn kernel routes; never install BGP routes |
| Import | `import_default4/6`: accept only `0.0.0.0/0` and `::/0`; `import limit 10 action block` | Vultr sends a default; accepting it keeps the session observable (1 route) but it is not installed because the provider default already exists. Anything more shuts the session. |
| Export | `export_anycast4/6`: `if ! ANNOUNCE then reject; if source != RTS_STATIC then reject; if net != <prefix> then reject;` | strict allowlist of exactly one prefix per family |
| Drain | `if DRAIN then` prepend AS215520 x3, add `65535:0` (GRACEFUL_SHUTDOWN, RFC 8326) and the upstream's prepend-x3 community (Vultr `20473:6003`) | route stays but becomes the worst path; traffic moves before withdraw |
| Forbidden | Vultr `20473:6000` (do not advertise globally) | never set; `netgen --check` and the tests fail if it appears |
| RPKI RTR | emitted commented out (`rtr.rpki.cloudflare.com:8282`, resolves as of 2026-09-10) | a leaf that imports only a default has nothing to validate; enable if the import policy grows |

Import of the default is the recommended choice over "accept nothing": with
`import none` a healthy session and a filtered-to-nothing session look the
same from `birdc`, and the default is harmless because the kernel export
filter drops it.

### 3.1 Announcement state and `bgp-announce`

`bird.conf` includes `state.conf`, which holds exactly two constants:

```
define ANNOUNCE = false;   # false: export nothing
define DRAIN = false;      # true: export with prepend + drain communities
```

`scripts/bgp-announce status|announce|withdraw|drain|undrain` rewrites the
file atomically (temp + rename), runs `birdc configure check`, then
`birdc configure`, and rolls the file back if either fails. It is idempotent
(no reload when nothing changes) so the health worker may call it every tick.
`drain`/`undrain` refuse to act while withdrawn. The generated `state.conf`
starts withdrawn: a freshly built node announces nothing until health says so.

### 3.2 Health-controlled announcement with hysteresis

The forge health worker (`internal/health`) evaluates local checks every
10 s: Gemini `20` on `/status` via the anycast address, SSH banner, WireGuard
peer reachability, SQLite integrity flag, and a "leader manifest not stale"
check. The state machine:

```
                 3 consecutive failures (30 s)
   ANNOUNCED  ----------------------------------->  DRAINING
      ^  |                                            |    |
      |  | operator: drain                            |    | 3 further failures (60 s total)
      |  v                                            |    v
      |  (same as DRAINING)                           |  WITHDRAWN
      |                                               |    |
      +-----------------------------------------------+----+
        6 consecutive successes (60 s) AND >= 120 s since last transition
        (WITHDRAWN -> ANNOUNCED goes through DRAINING for one tick
         so a flapping node never re-enters as the best path)
```

| Transition | Condition | Action |
| --- | --- | --- |
| healthy -> announced | K = 6 consecutive successes and cooldown 120 s elapsed | `bgp-announce announce` |
| announced -> draining | N = 3 consecutive failures | `bgp-announce drain` |
| draining -> withdrawn | M = 3 further consecutive failures | `bgp-announce withdraw` |
| draining -> announced | K successes and cooldown | `bgp-announce undrain` |
| withdrawn -> draining -> announced | K successes and cooldown | `announce` then `undrain` next tick |
| planned maintenance | operator | `drain`, wait 300 s, `withdraw`, do work, `announce` |

The cooldown doubles after each unplanned withdraw within an hour (120, 240,
480 s, capped at 900 s) so a POP that flaps at layer 7 stays out longer each
time. A POP that cannot reach any WireGuard peer for 5 minutes but is
otherwise healthy stays announced (reads still work from local replicas) and
raises an alert instead.

## 4. Anycast and TCP

- **Route changes mid-connection reset TCP.** A client whose path flips to
  another POP mid-stream gets a RST (the new POP has no such socket). Gemini
  and Titan are one request per connection, typically well under a second, so
  per-POP session state is fine and a reroute costs one retry. SSH sessions
  (git clone/push, interactive) live longer and will break on a reroute;
  git retries cheaply, and pushes are all-or-nothing on the leader.
- **Mitigations:** stable routing (no prepend/community churn under normal
  operation; hysteresis above), drain before withdraw so paths move while the
  old POP still answers, and `PersistentKeepalive` on the mesh so the leader
  path is warm. Long transfers should be retried by clients; Titan uploads are
  capped (256 MiB) and atomic.
- **Per-POP state is local by design** (TLS session tickets, rate limits,
  SQLite replica). A user who writes and immediately reads may hit a lagging
  replica; the daemon handles read-after-write by pinning the writing user to
  the leader for 30 s (threat model T-32).
- **Within one Vultr region** several announcers are ECMP-balanced; we run
  one node per region so this does not apply.

## 5. WireGuard mesh

Full mesh within a group (`production`: ewr1, ams1, ...; `lab` is separate).
Per POP `infra/wireguard/generated/<pop>/wg0.conf` (wg-quick):

- `Address = <mesh /128>/64`, `ListenPort 51820`, `MTU 1420`
  (1500 - 40 IPv6 - 8 UDP - 32 WireGuard), `PrivateKey = __WG_PRIVATE_KEY__`
  filled from `sops:infra/secrets/nodes/<pop>.enc.yaml#wg_private_key` at
  deploy.
- One `[Peer]` per other POP: `PublicKey` from `wireguard.public_keys` (or the
  overrides file; placeholder `__WG_PUBKEY_<pop>__` until registered),
  `Endpoint = [<provider IPv6>]:51820` (IPv4 fallback if a node has no IPv6),
  `AllowedIPs = <peer mesh /128>, <peer unicast /64>`,
  `PersistentKeepalive = 25`.
- Cryptokey routing is exact: a peer can only source its own mesh address and
  its own unicast /64 (threat model T-33). Keys are generated on the node and
  rotated on rebuild.
- nftables: allow UDP 51820 from `wg_endpoints_v6`/`wg_endpoints_v4`, allow
  forwarding from the public interface to `wg0` for `node_unicast_blocks_v6`
  only, and bind replication/metrics to `wg_mesh_v6`.

## 6. The /48 and Toronto (AS835) coexistence -- HUMAN DECISION

Today `2a0f:85c1:368::/48` is announced with 100 % visibility via AS835
(GoCodeIT / Xenyth, Toronto), origin AS215520. Announcing the same /48 from the
forge POPs makes Toronto one more anycast site for whatever is behind
`2a0f:85c1:368:1::1`; if nothing there answers on that address, Toronto's
catchment gets black-holed for the forge.

| Option | Effect | Assessment |
| --- | --- | --- |
| (a) keep Toronto, add POPs | Toronto becomes a POP without the forge; clients near Toronto hit a machine that does not run the service (or must be given the service too) | Bad unless Toronto is made a real POP (run the forge there, join the mesh). If Xenyth can be provisioned by OpenTofu/cloud-init, that is just "POP yto1 on provider xenyth". |
| (b) move all /48 usage to the forge POPs | Withdraw from AS835 when the first forge POP is healthy; anything currently served from Toronto on the /48 must move first | Cleanest routing outcome; requires an inventory of what Toronto serves today (the readiness report found `gemini.as215520.net` on Vultr addresses, not the /48, so possibly nothing). |
| (c) obtain another /48 for the forge | Toronto untouched; forge gets its own prefix (PI via a sponsoring LIR, or a second PA /48) | Costs money and time (PI: sponsoring-LIR fee, RIPE processing) but decouples the forge from the Inferno PA relationship (readiness item 4) and from Toronto entirely. |

Recommendation: **(b)**, with (c) as the strategic follow-up if the PA /48
risk is taken seriously. Concretely: stage the first POP, verify
`2a0f:85c1:368:1::1` from several vantage points while Toronto still
announces (Toronto catchment will show failures, which is the test), then
withdraw at AS835 and confirm via RIPEstat that all paths end at `20473 215520`.
The operator must confirm nothing else relies on the /48 in Toronto before the
withdraw. This is not something the pipeline decides.

## 7. Reverse DNS

Neither zone is delegated today. Plan:

| Zone | Delegation | Records |
| --- | --- | --- |
| `58.32.44.in-addr.arpa` | NS set in the ARDC portal to Cloudflare (`fay/kip.ns.cloudflare.com`) | `1` -> `git.as215520.net`; nothing for other addresses |
| `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` | `domain` object created by Inferno (or `mnt-domains: JOSHBOYD-MNT` added to the inet6num) pointing at Cloudflare | `1.0.0.0...1.0.0.0` (`:1::1`) -> `git.as215520.net`; `1.0.0.0...1.0.1.0` (`:101::1`) -> `ewr1.nodes.as215520.net`, etc. |

Both zones are hosted in Cloudflare first so the delegations resolve the
moment they are entered. PTRs for the mesh (ULA) are not published.
`netgen` does not yet emit PTR records; `rdns.ptr` in the plan fixes the
naming so it can be added without renaming anything.

## 7a. Service catalogue

`services:` in the address plan defines each service once; every POP lists the
services it runs by name, and `roles:` is left to the infrastructure roles
(`bgp`, `replica`, `lab`, `monitor`). A service declares:

| Field | Meaning |
| --- | --- |
| `critical` | a failed health check withdraws the POP from anycast |
| `anycast` | binds the anycast service addresses; needs a matching `ipv4/ipv6.services` address entry |
| `public_tcp` | admitted from the internet on every interface except `wg0` |
| `private_tcp` | admitted on `wg0` only |

`node_private_tcp` holds the mesh ports the platform opens regardless of
service, keyed by role (`all: [9101]` node-exporter, `bgp: [9324]`
bird-exporter). A POP's firewall is the union: its services' ports plus those.
`scripts/netgen --check` rejects an unknown service, a service name used as a
role, two services on one POP claiming the same port, a port that is both
public and private, an anycast service with no address, and a service on a
monitor.

**Anycast is per POP, not per service.** One health verdict decides whether a
node announces the prefixes, so everything on that node shares the fate of the
withdrawal. `critical` is what separates a service that may withdraw the POP
from one that must only alert; see ADR 0014.

## 8. What is generated from what

| Input | Output | Consumer |
| --- | --- | --- |
| `infra/network/address-plan.yaml` (`bgp`, `pops`, `ipv4`, `ipv6`) + overrides `pops.*.provider_ipv*` | `infra/bird/generated/<pop>/bird.conf` | BIRD on the node (`/etc/bird/bird.conf`), `bird -p` in tests |
| constant | `infra/bird/generated/<pop>/state.conf` | included by bird.conf; rewritten by `bgp-announce` |
| `wireguard`, `pops` + overrides `wireguard.public_keys`, `pops.*.provider_ipv*` | `infra/wireguard/generated/<pop>/wg0.conf` | wg-quick on the node after `__WG_PRIVATE_KEY__` is filled from SOPS |
| `zone`, `ipv4/ipv6.services`, `pops` (dns: true, non-lab) | `infra/network/generated/dns-nodes.yaml` | `infra/opentofu/modules/cloudflare-dns` variables `anycast_v4`, `anycast_v6`, `nodes` |
| `ipv4/ipv6`, `pops` (non-lab), `bgp.upstreams`, `wireguard`, `services`, `node_private_tcp` | `infra/network/generated/nftables-vars.nft` | host nftables ruleset (`include`); the `pop_<name>_*_tcp` sets cross-check what `modules/forge-node` renders |
| everything | `infra/network/generated/summary.md` | humans, PR review |

Placeholders left for the deploy step: `__BGP_MD5__` (from
`sops:infra/secrets/network.enc.yaml#vultr_bgp_password`), `__WG_PRIVATE_KEY__`
(per-node SOPS file), and, when no overrides were supplied, `__PROVIDER_IPV4__`,
`__PROVIDER_IPV6__`, `__WG_PUBKEY_<pop>__`, `__WG_ENDPOINT_<pop>__`. The
committed generated tree is produced without overrides; the tests generate
with `infra/network/overrides.example.yaml` (documentation addresses) and run
`bird -p` on the result.

Commands:

```
scripts/netgen --check                                   # validate the plan
scripts/netgen                                           # regenerate committed outputs
scripts/netgen --overrides infra/network/overrides.example.yaml --out-root /tmp/x
python3 -m unittest discover -s tests/network -v         # tests (bird -p if available)
```

## 9. Operator action list

Ordered by lead time; details and verification commands are in
`docs/network-readiness.md` ("Exact operator actions").

1. ARDC portal: ROA `44.32.58.0/24` maxLength 24 AS215520; LOA naming AS215520
   and Vultr AS20473; rDNS NS for `58.32.44.in-addr.arpa` (readiness 1a-1c).
2. Inferno: `domain` object for `8.6.3.0.1.c.5.8.f.0.a.2.ip6.arpa` or
   `mnt-domains: JOSHBOYD-MNT` (readiness 2a); decide on PI (2c).
3. Vultr: one live instance, then the BGP request form (ASN 215520, MD5
   password -> `infra/secrets/network.enc.yaml`, both prefixes, LOA upload,
   "Default Only"); wait for human review and BYOIP onboarding.
4. RIPE DB: `as-set AS215520:AS-ALL`; aut-num import/export for AS20473 and
   AS835 (readiness 4-5).
5. PeeringDB: `irr_as_set`, prefix counts 1/1, POCs (readiness 7).
6. Decide section 6 (Toronto) and schedule the AS835 withdraw.
7. First deploy: OpenTofu outputs -> overrides file -> `scripts/netgen`; SOPS
   fills placeholders; `bgp-announce status` should show withdrawn; health
   worker announces after the cooldown; verify with RIPEstat
   `looking-glass` and `rpki-validation` for both prefixes.
8. Cloudflare: apply `dns-nodes.yaml` through the dev environment; add CAA;
   publish the DS at Namecheap (readiness 8, 16).
