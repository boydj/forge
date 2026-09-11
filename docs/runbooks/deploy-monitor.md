# Deploy the monitoring host (mon1)

**Purpose**: bring up the dedicated Prometheus + blackbox_exporter + Grafana
host designed in `docs/monitoring.md`, join it to the WireGuard mesh, point
it at every POP, and turn on `bird_exporter` on the POPs so BGP session
state is scraped too. Everything is code: the address plan, an OpenTofu
instance, cloud-init, and `scripts/deploy monitor`.

**Preconditions**

- The POPs are live (`docs/status.md`) and `infra/network/address-plan.yaml`
  has the `mon1` entry (`roles: [monitor]`, index 250, region `ord`).
- Operator tooling from `deploy-first-node.md` (tofu, sops, age; the bundle
  `infra/secrets/dev.enc.yaml` and `dev.auto.tfvars`).
- Spend approved: one Vultr `vc2-1c-1gb` (~$5/month, `docs/costs.md`).
- `promtool` on the laptop is optional (`~/.local/bin`); the host checks the
  configuration itself before every reload.

**Duration**: 30-45 min (instance creation and two rounds of POP deploys
dominate). **Triggers**: first monitoring setup; a rebuilt monitor; a change
to `infra/monitoring/` (steps 5-6 only).

**What the host is**: a mesh member only. No anycast address, no BGP, no
forge daemon, no secret bundle. Its one secret is its WireGuard private key
(`/etc/wireguard/wg0.conf`, 0600 root). Prometheus and Grafana answer on
loopback (plus 9090 on wg0); you reach them with `ssh -L`.

## 1. WireGuard key for mon1

```sh
# laptop $
scripts/secrets gen-wg mon1                  # prints the YAML snippet and the PUBLIC key: keep both
scripts/secrets edit infra/secrets/dev.enc.yaml   # paste wireguard_private_keys.mon1
scripts/netgen --check                       # mon1 is a valid monitor entry
```

## 2. Create the instance (targeted apply, then a full apply for DNS)

Add the monitor to `infra/opentofu/environments/dev/dev.auto.tfvars`
(values from the address plan; see `dev.auto.tfvars.example`):

```hcl
monitors = {
  mon1 = { region = "ord", index = 250, wg_address = "fda5:bc65:9bb1:1::fa/64" }
}
```

```sh
# laptop $
eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"
tofu -chdir=infra/opentofu/environments/dev plan  -target='module.monitor["mon1"]'
tofu -chdir=infra/opentofu/environments/dev apply -target='module.monitor["mon1"]'   # creates the VM + its Vultr firewall group
tofu -chdir=infra/opentofu/environments/dev apply                                    # DNS mon1.nodes.<zone> (for_each needs the addresses first)
tofu -chdir=infra/opentofu/environments/dev output monitors                          # ipv4, ipv6, ssh and tunnel command lines
```

The second apply also refreshes `monitor_configs` (the rendered
`prometheus.yml` lists every created POP with its provider addresses).

## 3. Pin the host key and run the first deploy

cloud-init prints two console lines when it finishes (Vultr console):
`known_hosts: [mon1.nodes.<zone>]:2200 ssh-ed25519 ...` and
`hostkey fingerprint: SHA256:...`. Use either (SR-19b):

```sh
# laptop $
scripts/deploy monitor mon1 --hostkey-fingerprint SHA256:...      # or --known-hosts-line '<line>'
```

This, as root over the bootstrap key: pins the host, installs Grafana from
`apt.grafana.com` after checking the key against the fingerprint in
`infra/monitoring/grafana/apt-repo.conf`, pushes `prometheus.yml`, the
rules, `blackbox.yml`, the Grafana provisioning and dashboard, the
firewall and `wg0.conf` (with the key), then reloads/restarts the units and
prints their states, `/-/ready` and the wg0 handshakes (none yet: the POPs
do not know mon1 until step 4). Commit `infra/known_hosts`.

## 4. Register mon1 in the mesh and refresh the POPs

```sh
# laptop $
tofu -chdir=infra/opentofu/environments/dev output -json monitors | python3 -c 'import json,sys; m=json.load(sys.stdin)["mon1"]; print(m["ipv4"], m["ipv6"])'
```

Add to `infra/network/overrides.yaml`: `pops.mon1.provider_ipv4`,
`pops.mon1.provider_ipv6` (from above) and `wireguard.public_keys.mon1`
(the public key from step 1; or `ssh -p 2200 root@mon1.nodes.<zone> wg show
wg0 public-key`). Then regenerate and push, one POP at a time (each
`deploy <pop>` restarts forge for a few seconds; the health controller
keeps the announcement):

```sh
# laptop $
scripts/netgen --overrides infra/network/overrides.yaml     # POPs' wg0.conf now carry a real [Peer] mon1
for p in ewr1 ams1 sgp1; do
  scripts/deploy sysupdate $p        # root: installs prometheus-bird-exporter + our unit, enables it
  scripts/deploy $p                  # deploy user: nftables.conf (9324 on wg0), wg0.conf (mon1 peer), forge
done
```

Commit the regenerated `infra/wireguard/generated/*` and
`infra/network/generated/*` with the overrides change.

## 5. Verify

```sh
# laptop $
scripts/deploy monitor mon1                  # idempotent; expect every unit active, "/-/ready: 200", 3 wg0 handshakes
ssh -N -L 9090:127.0.0.1:9090 -L 3000:127.0.0.1:3000 -p 2200 deploy@mon1.nodes.<zone> &
curl -s http://127.0.0.1:9090/api/v1/targets | python3 -c 'import json,sys; [print(t["labels"]["job"], t["labels"].get("pop"), t["health"]) for t in json.load(sys.stdin)["data"]["activeTargets"]]'
```

Every target must be `up`. Spot checks with `promtool query instant
http://127.0.0.1:9090 '<expr>'` (or the Prometheus UI):

| Expression | Expect |
| --- | --- |
| `up == 0` | empty |
| `forge_healthy` | 1 for every POP |
| `forge_bgp_announced` | 1 for every POP |
| `bird_protocol_up{proto="BGP"}` | 1 for `vultr4` and `vultr6` on every POP |
| `probe_success{target_kind="anycast"}` | 1 for v4 and v6, port 1965 and 22 |
| `probe_success{target_kind="node"} == 0` | empty (provider addresses of every POP, v4 and v6) |
| `ALERTS{alertstate="firing"}` | empty, or only what you expect |

Grafana: `http://127.0.0.1:3000`, `admin` / `admin`, change the password
when asked; the dashboard is `forge overview` in folder `forge`.
Alerts: `http://127.0.0.1:9090/alerts` (no Alertmanager yet, see below).

## 6. Later changes

- Rules, dashboard, blackbox modules, Grafana settings: edit under
  `infra/monitoring/`, then `scripts/deploy monitor mon1`.
- A new POP: after its `tofu apply` and deploy, `tofu apply` again (so
  `monitor_configs` includes it), `scripts/netgen --overrides ...` and
  `scripts/deploy monitor mon1`; the new POP gets mon1 as a peer through
  its own deploy.
- Alert destination (**PLANNED**): install `prometheus-alertmanager` on
  mon1, set `alertmanager_targets = ["127.0.0.1:9093"]` in the `monitors`
  map, `tofu apply`, `scripts/deploy monitor mon1`. Until then alerts are
  visible only in the Prometheus UI.
- Grafana key rotation: update `GRAFANA_APT_KEY_FPR` in
  `infra/monitoring/grafana/apt-repo.conf`; the deploy only fetches the key
  when the package is not installed yet, so on an existing host refresh
  `/etc/apt/keyrings/grafana.gpg` by hand with the same fingerprint check.

## Rollback / removal

```sh
# laptop $
tofu -chdir=infra/opentofu/environments/dev destroy -target='module.monitor["mon1"]'
```

Then remove `pops.mon1` and `wireguard.public_keys.mon1` from
`infra/network/overrides.yaml`, `scripts/netgen --overrides ...`, and
`scripts/deploy <pop>` on each POP (the peer stanza goes back to a comment;
nothing else on a POP depends on mon1). The `bird_exporter` stays: it is
harmless and reachable from wg0 only.
