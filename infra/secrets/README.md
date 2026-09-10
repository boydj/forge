# infra/secrets

SOPS-encrypted (age) secret bundles, one per environment:
`dev.enc.yaml`, `staging.enc.yaml`, `production.enc.yaml`. Encrypted files
are committed; nothing else in this directory holds secret material.
Recipients are in `/.sops.yaml`; tooling is `scripts/secrets`;
the workflow is described in `docs/secrets.md`.

```
infra/secrets/
  README.md              this file (schema)
  schema.example.yaml    plaintext example with placeholder values (never real)
  <env>.enc.yaml         committed, SOPS/age encrypted
  nodes/<pop>.age.pub    node age recipients (public keys, committed)
```

No `.enc.yaml` file exists yet: create the first one with
`scripts/secrets init` (operator key) followed by
`scripts/secrets edit infra/secrets/dev.enc.yaml`, which opens
`schema.example.yaml` as the starting content.

## Schema (`schema_version: 1`)

| Key | Type | Used by | Notes |
|---|---|---|---|
| `schema_version` | int | tooling | Left unencrypted (`encrypted_regex`). |
| `vultr_api_key` | string | OpenTofu (`VULTR_API_KEY`) | Full-account key; restrict source IPs in the console. |
| `cloudflare_api_token` | string | OpenTofu (`CLOUDFLARE_API_TOKEN`) | Zone-scoped: `DNS:Edit` (+ `Zone:Read`). |
| `vultr_bgp_password` | string | BIRD on every Vultr POP | TCP-MD5 password chosen in the Vultr BGP form. Shipped to nodes as `/etc/forge/secrets/bgp.password`; substituted into `bird.conf` where the generated config carries `@@VULTR_BGP_PASSWORD@@`. |
| `wireguard_private_keys.<pop>` | map string | wg0 on `<pop>` | One X25519 key per POP (`scripts/secrets gen-wg <pop>` prints both halves; the public half goes in `infra/network/address-plan.yaml`). Shipped only to its own node as `/etc/forge/secrets/wg.key`, substituted where `wg0.conf` carries `@@WG_PRIVATE_KEY@@`. |
| `forge_tls_key` | PEM | forge (`gemini.key_file`) | ECDSA P-256 private key of the service certificate. Same on every POP (anycast, TOFU clients). `scripts/secrets gen-tls`. |
| `forge_tls_cert` | PEM | forge (`gemini.cert_file`) | Self-signed certificate for the service hostname, notAfter 2036-01-01. Not secret, kept with the key for atomic rotation. |
| `ssh_host_key` | OpenSSH private key | forge (`ssh.host_key_file`) | Ed25519 host key for Git-over-SSH, identical on every POP (SSHFP). `scripts/secrets gen-hostkey`. |
| `cluster_secret` | string | forge (`cluster.secret_file`) | Shared node-to-node RPC secret. 32 random bytes, hex. |
| `backup_encryption_key` | age identity | operator only | `AGE-SECRET-KEY-...`; the *public* recipient is derived and pushed to nodes as `/etc/forge/secrets/backup.recipient`; backups are decryptable only with this key. |

What each node receives from `scripts/deploy` (as a tar, encrypted with
`age -r <node recipient>`, decrypted on the node with `/etc/forge/age.key`):

```
/etc/forge/secrets/
  tls/server.key   tls/server.crt      forge_tls_key / forge_tls_cert
  ssh/host_ed25519                     ssh_host_key
  cluster.secret                       cluster_secret
  wg.key                               wireguard_private_keys.<pop>
  bgp.password                         vultr_bgp_password (Vultr POPs only)
  backup.recipient                     derived from backup_encryption_key
```

API keys never reach a node.

## Rules

* Never commit `*.key`, `*.pem`, plaintext YAML, or a `.tfvars` (git-ignored).
* Never add a real value to `schema.example.yaml`.
* Rotation: `scripts/secrets rotate <what>` describes the order for each key.
