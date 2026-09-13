# Running costs

Prices in USD/month unless noted. Vultr figures are from
`api.vultr.com/v2/plans` and Vultr docs as recorded in
`docs/research/vultr.md` section 5 and `infra/providers/vultr/facts.yaml`
(2026-09-10). Items marked *verify* could not be confirmed against a
primary source. **The ASN, the IPv6 /48 (RIPE, via the sponsoring LIR) and
the IPv4 /24 (ARDC) are already held by the operator; their fees are not
counted here.** Cloudflare DNS is the Free plan.

## Tiers

| | Minimal (1 node, no BGP) | Recommended production (3 POPs anycast) | Optional add-ons |
|---|---|---|---|
| Compute | 1 x `vc2-1c-1gb` (1 vCPU, 1 GB, 25 GB SSD, 1 TB) **$5.00** | 2 x Vultr `vhp-1c-1gb-intel` (1 GB, 25 GB NVMe, 2 TB) **$12.00** + 1 x second-provider BGP VPS (iFog / Servperso / Virtua class, 1 GB) **~$5-10** | Larger plan `vc2-1c-2gb` $10 each |
| Backups | none on Vultr (forge-backup + off-site below) | Backblaze B2 for `forge-backup` archives: $6/TB-month, first 10 GB free; three POPs at ~1 MB/night with 30-day retention is **$0** (`infra/opentofu/environments/b2`) | Vultr automatic backups +20% of plan ($1.00-1.20/instance); snapshots $0.05/GB-month (~$1.25 per 25 GB image) |
| Bandwidth | included 1 TB; overage $0.01/GB | included 2 TB per Vultr node; overage $0.01/GB | |
| DNS | Cloudflare Free **$0** | Cloudflare Free **$0** | |
| Domain | `as215520.net` **~$1/mo** ($12/yr at Namecheap) | same | brand domain, similar |
| DDoS protection | no | no (BGP withdraw / blackhole community 20473:666 is the tool) | Vultr DDoS protection **~$10-12 per instance** (*verify*; docs quote $10, third-party pages $12) |
| **Total** | **~$6** | **~$23-28** | |

Hourly billing applies (`vc2-1c-1gb` = $0.007/h), so a throwaway dev POP
for a working day costs about $0.20.

## Notes per line

* **Why `vhp-1c-1gb-intel` for production**: same $1 more than `vc2-1c-1gb`
  buys NVMe and 2 TB of egress, and it is offered in 32 of 33 regions
  (including `hnl`, which lacks `vc2`). BIRD + forge on a 1 GB node is
  comfortable; the systemd unit caps forge at 600 MB.
* **Third POP on a second provider**: required by the design (provider
  diversity, `docs/decisions/0006`), and none of the candidates has a
  Terraform provider, so it is provisioned by hand and then handled by the
  same `modules/forge-node` output + `scripts/deploy`. Candidate pricing is
  in `docs/research/vultr.md` section 11 (BuyVM 1 GB $3.50 incl. /48 but
  usually out of stock; Servperso from EUR 3.68; Virtua from EUR 5).
* **Backups**: `forge-backup.timer` writes an encrypted archive per day and
  keeps 7 locally (on the 25 GB disk); set `OFFSITE_CMD` in
  `/etc/default/forge-backup` to copy to object storage. 50 GB covers a
  month of daily archives for a forge of a few GB. Vultr's own instance
  backups are a poor fit (unencrypted, provider-bound, +20%).
* **Snapshots** are useful once, before a risky upgrade, and are billed
  $0.05/GB-month since 2023; delete them afterwards.
* **Bandwidth**: Git clones of large repositories are the only realistic
  way to exceed 1-2 TB; forge quotas (`limits.max_repo_bytes`) bound it.
* **Region surcharge**: `sao` costs $7.50 for `vc2-1c-1gb`; regions with
  manual transit filtering (India, Israel, parts of LatAm/Asia) also take
  longer to become reachable and are poor first-wave choices.
* **DDoS protection** is per instance and only protects the provider
  address; anycast BYOIP traffic is not documented as covered. The design
  relies on withdrawing a POP (health control / `scripts/deploy drain`) and
  on the `20473:666` blackhole community instead.
* **Not counted**: operator time; the RIPE membership/LIR sponsorship and
  ARDC allocation (already held); Cloudflare Registrar if the domain moves;
  a Vultr "Request Limit Increase" (free, human-reviewed) to run more than
  the initial instance cap.

## Scaling

Each extra Vultr POP is +$6 (`vhp-1c-1gb-intel`) or +$5 (`vc2-1c-1gb`); DNS
and secrets scale at zero cost; BGP on Vultr carries no fee beyond the
instance. Ten POPs worldwide are therefore ~$60-65 plus backups.

## Monitoring

| Item | Monthly | Notes |
| --- | --- | --- |
| Monitoring VM (Vultr `vc2-1c-1gb`, any region) | $5 | Prometheus + Grafana + blackbox exporter (`infra/monitoring/`); reachable over WireGuard only. Can be co-located on the first POP at $0 during Phase 1-2. |
| Alert delivery | $0 | Alertmanager to email (existing mailbox) or a Misfin/Gemini notice later; no paid pager in the minimal configuration. |
