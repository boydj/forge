# Self-hosting a forge node

Everything a node needs is one static binary (`forge`), one TOML file, a TLS
identity, an SSH host key, a systemd unit and a firewall. Two supported
paths produce identical nodes:

* **A. Vultr with our OpenTofu** (`infra/opentofu/environments/dev`): the
  instance is created with cloud-init from `modules/forge-node`, then
  `scripts/deploy` pushes the binary, configs and secrets.
* **B. Any Debian 13 box** you already have: `scripts/deploy bootstrap`
  performs the same preparation over SSH, then the same `scripts/deploy`.

Both end with a node listening on TCP 1965 (Gemini/Titan) and TCP 22
(Git over SSH), with OpenSSH for operators on **2200**, metrics on a private
address, and nightly maintenance + backup timers. Multi-POP anycast (BGP,
WireGuard mesh) is layered on top by the network tooling
(`infra/network/address-plan.yaml`, `scripts/netgen`) and is optional for a
single node.

## 0. Operator prerequisites

```sh
make tools-infra        # tofu, sops, age into ~/.local/bin
scripts/secrets init    # operator age key -> ~/.config/forge/age.key; prints the recipient
```

Put the recipient in `.sops.yaml` (replace `age1operatorplaceholder...`),
then create the environment bundle and fill it in:

```sh
scripts/secrets edit infra/secrets/dev.enc.yaml
# generate values to paste:
scripts/secrets gen-tls git.as215520.net      # forge_tls_key / forge_tls_cert (P-256, self-signed, until 2036)
scripts/secrets gen-hostkey git.as215520.net  # ssh_host_key (+ SSHFP line for DNS)
scripts/secrets gen-cluster                   # cluster_secret
scripts/secrets gen-backup-key                # backup_encryption_key (keep the private half offline too)
scripts/secrets gen-wg ewr1                   # wireguard_private_keys.ewr1 (multi-POP only)
```

See `docs/secrets.md` for the model and `infra/secrets/README.md` for the
schema. Single-node installs still need `forge_tls_key/cert`, `ssh_host_key`
and `backup_encryption_key`; the rest may stay as `REPLACE` (deploy skips
placeholder values with a warning).

## A. Vultr via OpenTofu

1. Credentials into the bundle (`vultr_api_key`, `cloudflare_api_token`), then:

   ```sh
   cd infra/opentofu/environments/dev
   cp dev.auto.tfvars.example dev.auto.tfvars
   $EDITOR dev.auto.tfvars        # bootstrap_ssh_public_key, create_node = true, addresses from address-plan.yaml
   eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
   tofu init && tofu plan && tofu apply
   tofu output pop                # ipv4, ipv6, admin ssh line
   ```

   What the instance does at first boot is listed in
   `infra/opentofu/modules/forge-node/README.md` (users, packages from Debian
   only, sshd to 2200, dummy0 addresses, nftables, units). Nothing is
   downloaded from GitHub or anywhere else by cloud-init.

2. Wait for `cloud-init status --wait` (or the console line
   `forge node ewr1 ready`), then from the repo root:

   ```sh
   scripts/deploy init-node ewr1    # fetches /etc/forge/age.pub -> infra/secrets/nodes/ewr1.age.pub (commit it)
   scripts/deploy ewr1              # build, push binary + units + configs + secrets, restart, smoke test
   ```

3. DNS for `ewr1.nodes.as215520.net` follows the instance automatically
   (`create_node = true` merges its addresses into the DNS module). The
   anycast `git.as215520.net` records point at the plan's anycast addresses;
   until BGP is enabled and the prefixes are onboarded (console steps in
   `docs/research/vultr.md` sections 1-2 and 10) the node is reachable on
   its `nodes.` name only. `scripts/deploy smoke ewr1` tests that name.

## B. Any Linux box, scripted

Requirements: Debian 13 (other systemd distributions work with package
name changes), root SSH access, a public IPv4 and/or IPv6.

```sh
# 1. prepare: packages, forge + deploy users, sshd -> 2200, dirs, node age key
FORGE_ADMIN_PORT=22 scripts/deploy bootstrap mybox --host root@203.0.113.9

# 2. from now on port 2200 and the deploy user
scripts/deploy init-node mybox --host deploy@203.0.113.9

# 3. render the node's forge.toml + nftables.conf once (no cloud needed):
mkdir -p var/mybox && cat > var/mybox/main.tf <<'EOT'
module "node" {
  source        = "../../infra/opentofu/modules/forge-node"
  name          = "mybox"
  hostname_fqdn = "mybox.example.org"
  service_hostname = "git.example.org"
}
output "forge_toml"    { value = module.node.forge_toml }
output "nftables_conf" { value = module.node.nftables_conf }
EOT
(cd var/mybox && tofu init -backend=false >/dev/null && tofu apply -auto-approve >/dev/null \
  && tofu output -raw forge_toml > forge.toml && tofu output -raw nftables_conf > nftables.conf)

# 4. deploy
scripts/deploy mybox --host deploy@203.0.113.9 --config-dir var/mybox
```

Without OpenTofu at all: copy `infra/cloud-init/forge.toml.tftpl` and
`infra/firewall/nftables.conf.tftpl`, replace the `${...}` placeholders by
hand (they are few and named), and pass that directory as `--config-dir`.

The systemd-networkd `dummy0` files, WireGuard and BIRD are only needed for
anycast; a single node skips them (the `forge.service` unit only *wants*
`wg-quick@wg0`, it does not require it).

## What `scripts/deploy <pop>` does

| Step | Detail |
|---|---|
| build | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build` (or `--binary`) |
| binary | scp to `~deploy/forge.new`, `install` to `/usr/local/bin/forge.new`, previous kept as `forge.prev`, `mv` into place (atomic) |
| units | `infra/systemd/*`, `infra/backup/forge-backup`, `scripts/bgp-announce` (if present), `daemon-reload` |
| configs | `forge.toml` + `nftables.conf` from `tofu output` (or `--config-dir`); `infra/bird/generated/<pop>/bird.conf`; `infra/wireguard/generated/<pop>/wg0.conf`; `state.conf` seeded only if the node has none |
| secrets | node's subset from the SOPS bundle, tar, `age -r <node recipient>`, scp, decrypted **on the node** with `/etc/forge/age.key` into `/run/forge/secrets/` (root:forge 0640); `__WG_PRIVATE_KEY__`, `__BGP_MD5__`, `__PROVIDER_IPV4/6__` placeholders filled |
| restart | `nft -c` + reload, `wg-quick@wg0` (if config complete), `bird -p` + `birdc configure`, `systemctl restart forge` |
| smoke | Gemini request `gemini://git.<zone>/status` via `openssl s_client` against the node's unicast name expecting a `2x`/`3x` header; SSH banner `SSH-2.0-...` on port 22 |

Other verbs: `rollback <pop>` (swap `forge.prev` back), `drain <pop>` /
`undrain <pop>` (run `bgp-announce withdraw|announce` on the node),
`smoke <pop>`, and `--dry-run` on everything.

## Files on a node

```
/usr/local/bin/forge            binary (+ forge.prev)
/etc/forge/forge.toml           config (root:forge 0640)
/run/forge/secrets/             tls/server.{key,crt} ssh/host_ed25519 cluster.secret wg.key bgp.password backup.recipient
/etc/forge/age.key              node identity (never leaves the node); age.pub = recipient
/var/lib/forge/                 forge.db, repos/, assets/, tmp/ (forge:forge 0750)
/var/backups/forge/             <stamp>.tar.gz.age (forge-backup.timer, daily; forge:forge 0700)
/etc/nftables.conf              input drop; 22/1965 public, 2200 rate-limited, 51820, 9100/9101/9200/9324 wg0-only
/etc/systemd/system/forge*.service|timer
/etc/bird/bird.conf, state.conf ; /etc/wireguard/wg0.conf     (anycast only)
```

## Checks after deploy

```sh
ssh -p 2200 deploy@ewr1.nodes.as215520.net 'systemctl status forge --no-pager; sudo nft list ruleset | head -40'
printf 'gemini://git.as215520.net/\r\n' | openssl s_client -quiet -connect ewr1.nodes.as215520.net:1965 -servername git.as215520.net
ssh -T git@ewr1.nodes.as215520.net            # expects the forge banner / "no shell" reply
```

## Known gaps

* `forge admin backup` and `forge admin maintenance` are the interfaces the
  timers and `infra/backup/forge-backup` call; they are not implemented yet.
* The `deploy` user has `NOPASSWD:ALL` sudo (key-only login on 2200,
  rate-limited). Narrowing it to a command allow-list is a TODO.
* Whether Vultr's Debian 13 image manages the primary NIC with ifupdown or
  netplan is unverified; cloud-init only gives `dummy0` to systemd-networkd
  so either works, but check `networkctl` on the first boot.
