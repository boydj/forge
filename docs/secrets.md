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
   node subset (tar)  --age -r <node recipient>-->  scp  -->  age -d -i /etc/forge/age.key
                                                             /etc/forge/secrets/*
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
* **Provider credentials** (`VULTR_API_KEY`, `CLOUDFLARE_API_TOKEN`) exist
  only in the environment of the `tofu` process:
  `eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"`. Never a
  variable, never in tfvars or state.

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
3. Bring up a node; `scripts/deploy init-node <pop>`; commit
   `infra/secrets/nodes/<pop>.age.pub`.
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
