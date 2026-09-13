# Secrets

Decision: `docs/decisions/0007-secrets-sops-age.md`. Schema:
`infra/secrets/README.md`. Tool: `scripts/secrets`.

## Model

```
 operator laptop                      git                          node
 ~/.config/forge/age.key  --sops-->  infra/secrets/<env>.enc.yaml
        |                             (committed, age-encrypted)
        | sops -d --extract (per key)
        v
   node subset (tar)  --age -r <node recipient>-->  ssh stdin  -->  /etc/forge/secrets.tar.age   (disk, root 0600)
                                                                        |  forge-secrets.service, at boot and on deploy:
                                                                        |  age -d -i /etc/forge/age.key | tar -x
                                                                        v
                                                                    /run/forge/secrets/*   (tmpfs, forge 0700)
                                                                    /run/forge/wg0.conf, /run/forge/bird.conf (rendered)
```

* **One bundle per environment**, YAML, every leaf encrypted (SOPS
  `unencrypted_regex` only exempts `schema_version`). Keys and comments stay
  readable, so diffs and reviews still make sense.
* **Recipients** (`.sops.yaml`): operator keys. Nodes are deliberately *not*
  recipients of the bundle; a node never sees API keys or other nodes'
  WireGuard keys. `scripts/deploy` extracts the node's subset and encrypts
  it to the node's own key.
* **Node keys** are generated on the node (cloud-init or `deploy bootstrap`)
  and never leave it. `scripts/deploy init-node <pop>` stores the *public*
  half in `infra/secrets/nodes/<pop>.age.pub` (committed).
* **Nothing decrypted rests on a node's disk** (threat model invariant I-18,
  security review SR-02). See "On the node" below.
* **Provider credentials** (`VULTR_API_KEY`, `CLOUDFLARE_API_TOKEN`) exist
  only in the environment of the `tofu` process:
  `eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"`. Never a
  variable, never in tfvars or state.

## On the node

Two files persist on the root filesystem, both root-only (0600):

| Path | What | Why it must persist |
|---|---|---|
| `/etc/forge/age.key` | the node's age identity | The one secret that has to survive a reboot: it is what turns the bundle below back into keys. It decrypts only *this node's* subset, never leaves the machine and is worthless without the bundle. A disk image therefore still yields this node's keys to whoever can read both files; the exposure is one node, not the fleet, and the answer is `rebuild-pop.md` plus rotation of what that node held. |
| `/etc/forge/secrets.tar.age` | the node's subset, age-encrypted to that key | Written by `scripts/deploy` through `forge-deploy-helper write-config secrets`. |

Everything else lives in tmpfs and is recreated by `forge-secrets.service`
(`infra/systemd/forge-secrets.service`, a root oneshot ordered `Before=`
`forge.service`, `bird.service` and `wg-quick@wg0.service`, restarted by
every `deploy`):

```
/run/forge/                       0755 root   (RuntimeDirectory of forge-secrets.service)
  secrets/                        0700 forge  tls/server.key tls/server.crt ssh/host_ed25519 cluster.secret backup.recipient mirror.key
  wg0.conf                        0600 root   /etc/wireguard/wg0.conf.in with __WG_PRIVATE_KEY__ filled; /etc/wireguard/wg0.conf -> here
  bird.conf                       0640 bird   /etc/bird/bird.conf.in with __BGP_MD5__ filled;           /etc/bird/bird.conf -> here
```

`forge.toml` points `cert_file`, `key_file`, `host_key_file`,
`secret_file` and `mirror.key_file` into `/run/forge/secrets/`;
`forge-backup.service` reads the backup recipient from there too.
`mirror.key` is the bundle's `mirror_deploy_key` (an ed25519 deploy key
with write access on the GitHub mirror, `docs/runbooks/enable-mirroring.md`);
when the bundle has none the file is absent and mirroring is disabled. The generated `bird.conf` / `wg0.conf`
stay on disk as `.in` files with their placeholders; while a placeholder is
unfilled (no secret shipped, or `netgen --overrides` not run) the rendered
file is removed and the unit fails to start, which is the safe direction
(nothing announced, no tunnel with a bogus key). `age` is the only tool the
node needs; `sops` never runs on nodes (Debian does not package it, and
pushing an unsigned binary from a laptop would add a supply-chain path).
The helper deletes the pre-tmpfs `/etc/forge/secrets/` directory on the
first `load-secrets` after the change.

## Commands

| Command | Purpose |
|---|---|
| `secrets init` | Create `~/.config/forge/age.key` (0600) and print the recipient. Back the key up offline. |
| `secrets edit <file>` | Create (seeded from `schema.example.yaml`) or edit a bundle in `$EDITOR`. |
| `secrets view / get <file> [key]` | Decrypt to stdout / one dotted key (`wireguard_private_keys.ewr1`). |
| `secrets env <file>` | `export VULTR_API_KEY=... CLOUDFLARE_API_TOKEN=...` lines. |
| `secrets gen-tls <host>` | ECDSA P-256 key + self-signed cert, `CN`/SAN = host, valid to 2036-01-01. |
| `secrets gen-hostkey [host]` | Ed25519 SSH host key + the SSHFP line for `tfvars`. |
| `secrets gen-wg <pop>` | WireGuard key pair (`wg` or OpenSSL X25519 fallback). |
| `secrets gen-cluster`, `gen-backup-key` | Cluster RPC secret; backup age identity. |
| `secrets updatekeys <file>` | Re-encrypt after changing `.sops.yaml`. |
| `secrets rotate <what>` | Prints the rotation runbook (tls, hostkey, wg, bgp, cluster, backup, api, operator). |

## Why these choices

* **TLS: P-256, self-signed, long-lived.** Gemini is TOFU; clients pin the
  certificate, so every rotation is a "changed identity" prompt for every
  user. A long validity (to 2036) turns rotation into a deliberate,
  announced event rather than a calendar chore. P-256 rather than Ed25519
  because several Gemini clients' TLS stacks (and older Go/OpenSSL builds)
  reject Ed25519 server certificates; RSA-2048 would also work but is
  slower and larger for no gain. The certificate is not secret but is kept
  next to the key so both rotate atomically.
* **One SSH host key for all POPs.** Anycast means clients cannot tell POPs
  apart; different keys would flap `known_hosts` and make SSHFP useless.
* **age over PGP/KMS.** No key server, no cloud dependency, tiny tooling,
  works on the node with the `age` Debian package alone (no `sops` on
  nodes).
* **Backups** are encrypted on the node to a recipient whose private half
  exists only with the operator (`backup_encryption_key`). A compromised
  node cannot read its own backup history.

## Bootstrap (first time)

1. `scripts/secrets init` on the operator machine, paste the recipient into
   `.sops.yaml`, commit `.sops.yaml`.
2. `scripts/secrets edit infra/secrets/dev.enc.yaml`; generate values with
   the `gen-*` commands; commit the encrypted file.
3. Bring up a node; `scripts/deploy init-node <pop> --hostkey-fingerprint
   SHA256:...` (fingerprint from the node console; pins the sshd host key in
   `infra/known_hosts`); commit `infra/secrets/nodes/<pop>.age.pub` and
   `infra/known_hosts`.
4. `scripts/deploy <pop>`.

Adding an operator: they run `secrets init`, you add their recipient to
`.sops.yaml`, run `secrets updatekeys` on every bundle, commit. Removing one:
the reverse, plus `secrets rotate` of everything they could read.

## Rules

* No private key or plaintext bundle is ever committed (`.gitignore` blocks
  `*.key`, `*.pem`, `*.tfvars`; CI greps for key headers and checks every
  `.enc.yaml` carries `sops:` metadata).
* `schema.example.yaml` contains placeholders only.
* Keep the operator age key offline-backed-up; losing it loses every bundle.
* The `deploy` user cannot read secrets and runs nothing as root except
  `forge-deploy-helper` (`infra/systemd/forge-deploy-helper`), which accepts
  only the binary, the data configs and the encrypted bundle on stdin.

## Backblaze B2 keys (off-site backups)

`b2_admin_key_id` / `b2_admin_key`: the operator's console key, used only
by OpenTofu on the laptop (`infra/opentofu/environments/b2`) and exported by
`scripts/secrets env`. `b2_backup_key_id` / `b2_backup_key`: the write-only
bucket key OpenTofu creates; `scripts/deploy` turns it into
`/run/forge/secrets/rclone.conf` on every node. Rotation: `tofu taint
b2_application_key.writer && tofu apply`, update the bundle, deploy.
