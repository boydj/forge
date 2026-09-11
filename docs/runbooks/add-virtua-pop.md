# Runbook: add a Virtua.Cloud POP (provider diversity, M9)

Purpose: bring up a forge POP on Virtua.Cloud (AS35661) so the anycast no
longer depends on Vultr alone. Virtua offers free customer-ASN BGP sessions
and BYOIP self-service; the /24 and /48 meet its minimum sizes.

What the API can and cannot do (verified against Virtua.Cloud API v1.0.2,
`X-API-Key`, https://api.virtua.cloud/v1/):

- **Can**: list projects/offers/systems, create/start/stop/delete a Cloud
  Server, read its root password. `scripts/virtua` wraps these.
- **Cannot**: the create call has **no SSH-key or cloud-init field**, so a
  new server boots with a root password and must be bootstrapped over SSH
  (`scripts/deploy bootstrap`), not via cloud-init like Vultr. The published
  API has **no BGP/BYOIP endpoint**: the BGP session is requested in the
  customer area, and its peer addresses are shown there, not via API.

Preconditions (operator; one-time):

1. Create a Virtua.Cloud account, add a payment method (Cloud Server ~EUR 5/mo;
   BGP session and BYOIP are free). **Spend approval required.**
2. Customer area > API: generate a key. Store it:
   `scripts/secrets edit infra/secrets/dev.enc.yaml` -> `virtua_api_key: "..."`.
3. Confirm authorization for the prefixes. The /48 has a valid RIPE ROA; the
   /24 (ARDC 44Net) has **no ROA** (ADR 0013). Check whether Virtua accepts an
   origin-unvalidated /24; if not, this POP announces only the /48 (v6).

Steps:

1. Pick a region for `scripts/virtua offers <region>` (Paris, Lille,
   Frankfurt, Amsterdam, Fremont). Recommended: **Fremont** (US-West +
   provider diversity; Amsterdam would overlap the Vultr ams1 POP). Name the
   POP `sfo1`.
2. Provision the VM:
   ```
   eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"
   scripts/virtua projects            # PROJECT_UUID
   scripts/virtua offers fremont      # OFFER_UUID (1 vCPU / 1 GB)
   scripts/virtua systems             # SYSTEM_UUID (Debian 13)
   scripts/virtua create --project P --offer O --system S --hostname sfo1
   scripts/virtua wait <UUID>; scripts/virtua show <UUID>      # note IPv4/IPv6
   scripts/virtua password <UUID>                              # root password
   ```
3. WireGuard key + plan entry: `scripts/secrets` gen a key into
   `wireguard_private_keys.sfo1`, put its public key in
   `infra/network/overrides.yaml`, and add to `infra/network/address-plan.yaml`
   a `pops` entry `{name: sfo1, provider: virtua, index: 4, ipv6_unicast_block:
   2a0f:85c1:368:104::/64, wg_address: fda5:bc65:9bb1:1::4, ...}` plus a
   `bgp.upstreams.virtua` entry.
4. Request the BGP session in the Virtua customer area (customer ASN
   AS215520, prefixes 44.32.58.0/24 and 2a0f:85c1:368::/48). Copy the
   session's peer IPv4/IPv6, Virtua ASN (35661) and any MD5 into
   `bgp.upstreams.virtua` in the address plan (peer addresses are only shown
   there; `netgen --check` rejects placeholders, so fill the real values).
   Store the MD5 (if any) as `virtua_bgp_password` in the bundle.
5. `scripts/netgen --overrides infra/network/overrides.yaml` (BIRD/WireGuard
   for sfo1; peers of the existing POPs update too). `python3 -m unittest
   discover -s tests/network`.
6. Bootstrap and deploy (no cloud-init on Virtua):
   ```
   scripts/deploy bootstrap sfo1 --host root@<ip> --hostkey-fingerprint SHA256:...
   scripts/deploy init-node sfo1
   scripts/deploy sfo1
   ```
   `deploy bootstrap` installs packages, the `forge`/`deploy` users, sshd on
   2200, the helper, units and the node age key over the password SSH.
7. Add AS35661 to the aut-num and apply:
   `infra/network/irr/aut-num-AS215520.rpsl` (mp-import/mp-export for AS35661),
   then `tofu -chdir=infra/opentofu/environments/ripe apply -var dry_run=false`.
8. Verify replication (`forge admin pop status` on sfo1 = ok), then set
   `bgp_announce = true` for sfo1 in the pops map and `scripts/deploy sfo1`;
   confirm with `scripts/netcheck --expect announced` and the looking glass
   (paths ending `35661 215520` alongside `20473 215520`).

Rollback: `scripts/virtua delete <UUID>`; remove the sfo1 entry from the
address plan, overrides, bundle and aut-num; `scripts/netgen` and re-apply.

DNS: add sfo1 to the environment's node map so `sfo1.nodes.as215520.net`
resolves (it is not created by `scripts/virtua`; add it as a static `nodes`
entry or via the Cloudflare module).
