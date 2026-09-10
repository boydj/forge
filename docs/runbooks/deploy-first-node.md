# Deploy the first node from zero

**Purpose**: from an empty laptop and an empty cloud account to one forge
node answering on `<pop>.nodes.<zone>`, with DNS records published.
Anycast/BGP is a separate step: `network-bootstrap.md`.

**Preconditions**

- Debian/Linux laptop with Go, `git`, `openssl`, `ssh` (+ `ssh-keyscan`), `python3` + PyYAML.
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

Wait for first boot to finish and **read the host key from the console**,
not over the network. Vultr exposes no host-key API (`vultr-cli instance
get` and `tofu output` have nothing), but cloud-init writes three lines to
the serial console at the end of first boot (Vultr portal: instance ->
*View Console*; cloud-init's own "SSH HOST KEY FINGERPRINTS" block is there
as well):

```
forge node <pop> (leader, <pop>.nodes.<zone>) bootstrapped; admin ssh :2200; age recipient age1...
known_hosts: [<pop>.nodes.<zone>]:2200 ssh-ed25519 AAAAC3...
hostkey fingerprint: SHA256:...
```

Either line pins the key in step 3. Without any console the fallback is a
deliberate trust-on-first-use: `ssh-keyscan -p 2200 -t ed25519
<pop>.nodes.<zone>` and pass its output as `--known-hosts-line`, noting in
the commit that the key was not verified out of band (SR-19b). Only after
the key is pinned:

```sh
ssh -p 2200 -o UserKnownHostsFile=infra/known_hosts root@<pop>.nodes.<zone> 'cloud-init status --wait; cat /etc/forge/node.env; networkctl'
```

## 3. Pin the host key, enrol the node's age key, deploy

```sh
scripts/deploy init-node <pop> --hostkey-fingerprint SHA256:...        # from the console; ssh-keyscan + compare, then pin
#   or: scripts/deploy init-node <pop> --known-hosts-line '[<pop>.nodes.<zone>]:2200 ssh-ed25519 AAAA...'
#   both write infra/known_hosts (public, committed) and infra/secrets/nodes/<pop>.age.pub
git add infra/known_hosts infra/secrets/nodes/<pop>.age.pub && git commit -m "<pop>: host key + node recipient"
scripts/netgen                            # bird.conf / state.conf / wg0.conf under infra/*/generated/<pop>
scripts/deploy <pop> --dry-run            # review the plan
scripts/deploy <pop>                      # build, push binary + configs + secrets through forge-deploy-helper, apply, smoke
```

Every later `deploy`, `drain`, `rollback`, `status` refuses a host that is
not in `infra/known_hosts` (`StrictHostKeyChecking=yes`); a rebuilt node
gets a new key, so delete its line and pin again (`rebuild-pop.md`).

`deploy` renders `forge.toml` and `nftables.conf` from `tofu output` and
pushes everything on the stdin of `forge-deploy-helper`, the only command
the `deploy` user may sudo. On the node `forge-secrets.service` decrypts the
bundle into `/run/forge` (tmpfs) and renders `wg0.conf` / `bird.conf` from
the placeholder files; the BIRD and WireGuard files carry `__PLACEHOLDER__`
tokens until `netgen --overrides` has the provider addresses, and the
helper leaves `wg-quick`/`bird` alone while placeholders remain. That is
expected for a first, single node. Systemd units, the helper itself,
`bgp-announce` and `forge-backup` come from cloud-init; when they change in
the repo, refresh them as root with `scripts/deploy sysupdate <pop>` (uses
the bootstrap key), then `deploy <pop>`.

## 4. Verify

```sh
scripts/deploy smoke <pop>                                   # gemini 2x/3x on /status, SSH-2.0 banner
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect <pop>.nodes.<zone>:1965 -servername git.<zone> 2>/dev/null | head -3
scripts/deploy status <pop>                                  # unit states (forge, forge-secrets, bird, wg0, nftables, timers), last log lines
# ad hoc inspection is done as root (bootstrap key); the deploy user has no general sudo:
ssh -p 2200 -o UserKnownHostsFile=infra/known_hosts root@<pop>.nodes.<zone> 'runuser -u forge -- forge admin status; ls -la /run/forge /run/forge/secrets; ls -l /etc/forge; nft list ruleset | head -40'
ssh -p 2200 -o UserKnownHostsFile=infra/known_hosts root@<pop>.nodes.<zone> 'journalctl -u forge -n 20 --no-pager | grep -E "tls identity|ssh host key|listening"'
dig +short A git.<zone>; dig +short AAAA git.<zone>; dig +short SSHFP git.<zone>; dig +short AAAA <pop>.nodes.<zone>
```

Expected on the node: `/etc/forge` holds only `forge.toml`, `node.env`,
`age.key`, `age.pub` and `secrets.tar.age` (no `secrets/` directory);
`/run/forge/secrets` is `0700 forge` with the TLS, host and cluster files;
`/etc/wireguard/wg0.conf` and `/etc/bird/bird.conf` are symlinks into
`/run/forge` once their placeholders are filled.

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
  `git.<zone>` stays). Remove `infra/secrets/nodes/<pop>.age.pub` and the
  node's line in `infra/known_hosts` if the node will be recreated (the new
  one gets new keys).
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
