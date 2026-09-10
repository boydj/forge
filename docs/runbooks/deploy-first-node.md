# Deploy the first node from zero

**Purpose**: from an empty laptop and an empty cloud account to one forge
node answering on `<pop>.nodes.<zone>`, with DNS records published.
Anycast/BGP is a separate step: `network-bootstrap.md`.

**Preconditions**

- Debian/Linux laptop with Go, `git`, `openssl`, `ssh`, `python3` + PyYAML.
- Vultr account with the API enabled, Cloudflare zone `<zone>` with an API
  token (`Zone:DNS:Edit` + `Zone:Zone:Read`), see
  `infra/opentofu/environments/dev/README.md`.
- An SSH key for the `deploy` user (`~/.ssh/id_ed25519.pub`).
- Address plan entries for the POP in `infra/network/address-plan.yaml`.

**Duration**: 45-90 min (Vultr provisioning and cloud-init dominate).

**Triggers**: new environment; total loss (`restore-from-backup.md` first
sends you here).

## 1. Operator tooling and secrets

```sh
# laptop $
make tools-infra                       # tofu, sops, age into ~/.local/bin
scripts/secrets init                   # writes ~/.config/forge/age.key, prints "recipient: age1..."
```

Put the printed recipient into `.sops.yaml` (replace
`age1operatorplaceholder...` in **both** rules), commit `.sops.yaml`, then
back up `~/.config/forge/age.key` offline (losing it loses every bundle).

Create the bundle and generate every value it needs:

```sh
scripts/secrets gen-tls git.<zone>        # forge_tls_key / forge_tls_cert (+ fingerprint: save it for the front page)
scripts/secrets gen-hostkey git.<zone>    # ssh_host_key (+ SSHFP "4 2" line: save it for tfvars)
scripts/secrets gen-cluster               # cluster_secret
scripts/secrets gen-backup-key            # backup_encryption_key (keep the AGE-SECRET-KEY offline as well)
scripts/secrets gen-wg <pop>              # wireguard_private_keys.<pop>; public key -> address-plan.yaml
scripts/secrets edit infra/secrets/dev.enc.yaml   # paste all of the above plus vultr_api_key, cloudflare_api_token, vultr_bgp_password
scripts/secrets view infra/secrets/dev.enc.yaml | grep -c REPLACE   # 0 expected; deploy skips REPLACE values with a warning
git add .sops.yaml infra/secrets/dev.enc.yaml && git commit -m "secrets: dev bundle"
```

## 2. DNS first, then the instance (`create_dns` / `create_node`)

```sh
cd infra/opentofu/environments/dev
cp dev.auto.tfvars.example dev.auto.tfvars
$EDITOR dev.auto.tfvars
#   bootstrap_ssh_public_key = "<your ssh-ed25519 public key>"
#   sshfp = [ { algorithm = 4, type = 2, fingerprint = "<hex from gen-hostkey>" } ]
#   create_dns  = true
#   create_node = false          # step 2a
eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
tofu init && tofu plan && tofu apply       # 2a: anycast A/AAAA/SSHFP for git.<zone>
```

If `git.<zone>` records already exist by hand, import them instead of
recreating (README "First apply against an existing zone").

```sh
sed -i 's/^create_node *= *false/create_node  = true/' dev.auto.tfvars
tofu plan && tofu apply                    # 2b: vultr_ssh_key, firewall group, instance with cloud-init
tofu output pop                            # ipv4, ipv6, admin ssh line
unset VULTR_API_KEY CLOUDFLARE_API_TOKEN
cd -
```

`create_node = true` merges the instance addresses into the DNS `nodes`
map, so `<pop>.nodes.<zone>` follows the instance automatically.

Wait for first boot to finish:

```sh
ssh -p 2200 root@<pop>.nodes.<zone> 'cloud-init status --wait; cat /etc/forge/node.env; networkctl'
```

## 3. Enrol the node's age key and deploy

```sh
scripts/deploy init-node <pop>            # writes infra/secrets/nodes/<pop>.age.pub (commit it)
git add infra/secrets/nodes/<pop>.age.pub && git commit -m "secrets: <pop> node recipient"
scripts/netgen                            # bird.conf / state.conf / wg0.conf under infra/*/generated/<pop>
scripts/deploy <pop> --dry-run            # review the plan
scripts/deploy <pop>                      # build, push binary + units + configs + secrets, restart, smoke
```

`deploy` renders `forge.toml` and `nftables.conf` from `tofu output`; the
BIRD and WireGuard files carry `__PLACEHOLDER__` tokens until
`netgen --overrides` has the provider addresses, and `restart_services`
skips `wg-quick`/`bird` while placeholders remain. That is expected for a
first, single node.

## 4. Verify

```sh
scripts/deploy smoke <pop>                                   # gemini 2x/3x on /status, SSH-2.0 banner
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect <pop>.nodes.<zone>:1965 -servername git.<zone> 2>/dev/null | head -3
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo -u forge forge admin status; systemctl is-active forge forge-backup.timer forge-maintenance.timer; sudo nft list ruleset | head -40'
ssh -p 2200 deploy@<pop>.nodes.<zone> 'journalctl -u forge -n 20 --no-pager | grep -E "tls identity|ssh host key|listening"'
dig +short A git.<zone>; dig +short AAAA git.<zone>; dig +short SSHFP git.<zone>; dig +short AAAA <pop>.nodes.<zone>
```

Expected: `/status` answers `20 text/plain` then `ok <node> <version>` (or
`41 unhealthy: starting` for the first ~10 s); the logged TLS fingerprint
matches what `gen-tls` printed; `git.<zone>` resolves to the anycast
addresses from the plan (not yet routed: normal until
`network-bootstrap.md`).

First account: the first certificate registered at `gemini://<pop>.nodes.<zone>/account`
becomes administrator. Alternatively `forge admin user create <name> --admin`
then `forge admin cert enrol-code <name>` and enter the code at `/account`.

## Rollback

- Instance wrong: `create_node = false`, `tofu apply` destroys it (DNS for
  `git.<zone>` stays). Remove `infra/secrets/nodes/<pop>.age.pub` if the
  node will be recreated (the new one gets a new key).
- Whole environment: `tofu destroy` (DNS included).
- Nothing on the laptop needs undoing except a bad bundle: `scripts/secrets
  edit` again.

## Known gaps

- `/etc/forge/forge.toml` as rendered has no `[health]` section, so the
  daemon's health worker runs with a **no-op announcer** (`docs/health.md`,
  `Announcer` empty). Until the template carries
  `[health] announcer = "/usr/local/bin/bgp-announce"`, announcement is
  manual (`drain-pop.md`). **PLANNED**.
- `cluster.peers` is described as "appended by scripts/deploy" in the
  template comment; `deploy` does not do that today. For a multi-node
  cluster render `forge.toml` yourself and pass `--config-dir`. **PLANNED**.
