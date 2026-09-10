# Rotate secrets

**Purpose**: rotate the non-identity secrets in
`infra/secrets/<env>.enc.yaml`: operator age recipients, node age keys,
WireGuard keys, the cluster secret, the BGP MD5 password, the backup key,
and provider API tokens. TLS and host key have their own runbooks.
`scripts/secrets rotate <what>` prints the short form of each.

**Preconditions**: operator age key; every affected POP reachable on 2200
and pinned in `infra/known_hosts`. Node-side commands below go through
`scripts/deploy` (the `deploy` user has no general sudo); anything ad hoc is
`ssh -p 2200 -o UserKnownHostsFile=infra/known_hosts root@<pop>.nodes.<zone>`.

**Duration**: 5-30 min each; WireGuard and cluster secret touch every POP.

**Triggers**: operator leaves; node compromise (`rebuild-pop.md`); token
leaked or expired; annual hygiene for API tokens.

Every rotation ends with `git commit` of the re-encrypted bundle and
`scripts/deploy <pop>` for the nodes that consume the value (`--binary
bin/forge` after one `make build` avoids rebuilding per POP).

## Operator age recipients (add or remove an operator)

```sh
# new operator, on their machine:
scripts/secrets init                                  # prints recipient: age1...
# existing operator:
$EDITOR .sops.yaml                                    # add (or remove) the recipient in BOTH creation_rules
for f in infra/secrets/*.enc.yaml; do scripts/secrets updatekeys "$f"; done
git commit -am "secrets: recipients" && git push
scripts/secrets view infra/secrets/dev.enc.yaml >/dev/null && echo ok      # still decrypts for you
```

Removing an operator: they could read everything, so also rotate every
value below plus `rotate-tls-certificate.md` and
`rotate-ssh-host-key.md`. Provider tokens first (they need no deploy).

Verify: the new operator runs `scripts/secrets view` successfully; the
removed one's key no longer appears in `sops -d --extract '["sops"]'`
output (`grep -c age1` on the file's `sops:` block).

Rollback: re-add the recipient, `updatekeys`.

## Node age key (re-enrol a node)

Generated on the node, never leaves it; it is the one secret that persists
on the node's disk because it is what decrypts `/etc/forge/secrets.tar.age`
into tmpfs at every boot (`docs/secrets.md`, "On the node"). To rotate:

```sh
scripts/deploy init-node <pop> --rotate-key           # node: age.key -> age.key.old, new key; records the new recipient in infra/secrets/nodes/<pop>.age.pub
git commit -am "secrets: re-enrol <pop>"
scripts/deploy <pop> --binary bin/forge               # re-ships the subset encrypted to the new key; forge-secrets accepts either key meanwhile
```

The helper deletes `age.key.old` itself once the freshly shipped bundle
decrypts with the new key alone (end of `apply`), so a reboot between the
two steps still brings the node up. Verify: `scripts/deploy status <pop>`
shows `forge-secrets active`; as root, `ls /etc/forge` has no `age.key.old`.

If the node was ever listed in `.sops.yaml`, remove it and `updatekeys`.

## WireGuard key of one POP

```sh
scripts/secrets gen-wg <pop>                          # private key YAML + public key
scripts/secrets edit infra/secrets/dev.enc.yaml       # wireguard_private_keys.<pop>
$EDITOR infra/network/address-plan.yaml               # pops.<pop>.wireguard.public_key
scripts/netgen                                        # regenerates wg0.conf for EVERY pop (peers carry the new public key)
git commit -am "wireguard: rotate <pop>"
scripts/deploy <pop> --binary bin/forge               # its own private key
for peer in <other pops>; do scripts/deploy $peer --binary bin/forge; done   # their wg0.conf
```

The mesh is broken between the rotated POP and each peer until that peer
is deployed; replication and metrics pause meanwhile (reads keep serving
from local copies). Verify with `scripts/deploy status <pop>` on each node
(wg0 peers with a recent `latest handshake`) and `forge admin pop status`
(rows return to `ok`).

Rollback: put the old private/public pair back in the bundle and plan,
`netgen`, redeploy all.

## Cluster secret

```sh
scripts/secrets gen-cluster                           # cluster_secret: "<64 hex>"
scripts/secrets edit infra/secrets/dev.enc.yaml
git commit -am "secrets: rotate cluster secret"
for pop in <all pops>; do scripts/deploy $pop --binary bin/forge; done     # replication fails with 401 until all agree
```

`docs/replication.md` refers to `forge admin cluster init` for the secret;
that command does not exist, `gen-cluster` is the source. **PLANNED**.

Verify: no `control auth failed` in `journalctl -u forge` on any node;
`pop status` rows `ok`. Rollback: old value back, redeploy all.

## BGP MD5 password (Vultr POPs)

```sh
# 1. change it on Vultr's side (BGP page / support ticket), then:
scripts/secrets edit infra/secrets/dev.enc.yaml       # vultr_bgp_password
git commit -am "secrets: rotate BGP password"
for pop in <vultr pops>; do
  scripts/deploy drain $pop                            # session will flap
  scripts/deploy $pop --binary bin/forge               # forge-secrets renders __BGP_MD5__ into /run/forge/bird.conf, birdc configure
  scripts/deploy status $pop | grep -i bgp             # Established
  scripts/deploy undrain $pop
done
```

## Backup encryption key

```sh
scripts/secrets gen-backup-key                        # new AGE-SECRET-KEY + recipient
scripts/secrets edit infra/secrets/dev.enc.yaml       # backup_encryption_key = new; KEEP the old one as backup_encryption_key_prev
git commit -am "secrets: rotate backup key"
for pop in <all pops>; do scripts/deploy $pop --binary bin/forge; done     # new /run/forge/secrets/backup.recipient once forge-secrets restarts
ssh -p 2200 -o UserKnownHostsFile=infra/known_hosts root@<pop>.nodes.<zone> 'systemctl start forge-backup.service; ls -l /var/backups/forge | tail -2'
```

Archives already on disk are encrypted to the **old** recipient: keep the
old identity (offline and as `_prev`) for as long as those archives
matter (7 days on-node, longer off-site). Verify by decrypting the newest
archive with the new key (`restore-from-backup.md` step 0).

## Provider API tokens (Vultr, Cloudflare)

```sh
# regenerate in the provider console (Vultr: Account -> API, restrict source IPs; Cloudflare: My Profile -> API Tokens, zone-scoped)
scripts/secrets edit infra/secrets/dev.enc.yaml       # vultr_api_key, cloudflare_api_token
git commit -am "secrets: rotate provider tokens"
cd infra/opentofu/environments/dev && eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)" && tofu plan   # "No changes" proves both tokens work
unset VULTR_API_KEY CLOUDFLARE_API_TOKEN; cd -
```

Nothing to deploy; tokens never reach a node. Revoke the old token in the
console after `tofu plan` succeeds.
