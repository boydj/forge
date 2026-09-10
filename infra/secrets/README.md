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
| `vultr_bgp_password` | string | BIRD on every Vultr POP | TCP-MD5 password chosen in the Vultr BGP form. Rendered by `forge-secrets.service` into `/run/forge/bird.conf` (tmpfs) where the generated config carries `__BGP_MD5__`; never on disk in clear. |
| `wireguard_private_keys.<pop>` | map string | wg0 on `<pop>` | One X25519 key per POP (`scripts/secrets gen-wg <pop>` prints both halves; the public half goes in `infra/network/address-plan.yaml`). Shipped only to its own node; rendered by `forge-secrets.service` into `/run/forge/wg0.conf` (tmpfs) where `wg0.conf` carries `__WG_PRIVATE_KEY__`. |
| `forge_tls_key` | PEM | forge (`gemini.key_file`) | ECDSA P-256 private key of the service certificate. Same on every POP (anycast, TOFU clients). `scripts/secrets gen-tls`. |
| `forge_tls_cert` | PEM | forge (`gemini.cert_file`) | Self-signed certificate for the service hostname, notAfter 2036-01-01. Not secret, kept with the key for atomic rotation. |
| `ssh_host_key` | OpenSSH private key | forge (`ssh.host_key_file`) | Ed25519 host key for Git-over-SSH, identical on every POP (SSHFP). `scripts/secrets gen-hostkey`. |
| `cluster_secret` | string | forge (`cluster.secret_file`) | Shared node-to-node RPC secret. 32 random bytes, hex. |
| `backup_encryption_key` | age identity | operator only | `AGE-SECRET-KEY-...`; the *public* recipient is derived and shipped to nodes (`/run/forge/secrets/backup.recipient`); backups are decryptable only with this key. |

What each node receives from `scripts/deploy`: one file,
`/etc/forge/secrets.tar.age` (root 0600), a tar of the subset below encrypted
with `age -r <node recipient>`. `forge-secrets.service` decrypts it with
`/etc/forge/age.key` into tmpfs at boot and on every deploy
(`docs/secrets.md`, "On the node"):

```
/run/forge/secrets/                    tmpfs, 0700 forge
  tls/server.key   tls/server.crt      forge_tls_key / forge_tls_cert
  ssh/host_ed25519                     ssh_host_key
  cluster.secret                       cluster_secret
  backup.recipient                     derived from backup_encryption_key
/run/forge/wg0.conf                    wireguard_private_keys.<pop> rendered into wg0.conf.in (0600 root)
/run/forge/bird.conf                   vultr_bgp_password rendered into bird.conf.in (0640 root:bird; Vultr POPs only)
```

API keys never reach a node; nothing decrypted touches a node's disk.

## Rules

* Never commit `*.key`, `*.pem`, plaintext YAML, or a `.tfvars` (git-ignored).
* Never add a real value to `schema.example.yaml`.
* Rotation: `scripts/secrets rotate <what>` describes the order for each key.
