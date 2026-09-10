# Rebuild a POP

**Purpose**: destroy and recreate a node's instance, re-enrol its age key,
and repopulate data from the leader (replica) or from backup (leader or
single node). Also the last step of key-compromise handling
(`docs/tls.md`).

**Preconditions**: `infra/secrets/dev.enc.yaml` and the operator age key;
for a leader or single node, a backup archive plus the backup private key
(`restore-from-backup.md`); for a replica, a healthy leader.

**Duration**: 1-2 h (provisioning, cloud-init, deploy, restore/sync,
BGP re-entry).

**Triggers**: VM lost or disk dead, node compromised, distribution
upgrade, `move-leader.md` decommission reversed.

## 0. Take it out of service

```sh
scripts/deploy drain <pop>                    # bgp-announce withdraw, if the node still answers
# untrusted node: withdraw from the Vultr portal / `tofu` destroy below removes the instance entirely
```

If the node **led** repositories and is dead, writes to those are refused
until it is back or leadership moves. `move-leader` needs the leader to
run; with a dead leader the recovery is to restore the leader (this
runbook) rather than to promote a replica. **PLANNED**: leader promotion
from a replica.

## 1. Destroy and recreate the instance

```sh
cd infra/opentofu/environments/dev
eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
tofu taint 'module.pop[0].vultr_instance.this'
tofu apply                                    # replaces the instance; <pop>.nodes.<zone> follows the new addresses
tofu output pop
unset VULTR_API_KEY CLOUDFLARE_API_TOKEN; cd -
ssh-keygen -R '[<pop>.nodes.<zone>]:2200'     # new host key for the operator port
ssh -p 2200 root@<pop>.nodes.<zone> 'cloud-init status --wait'
```

Hand-installed box: `FORGE_ADMIN_PORT=22 scripts/deploy bootstrap <pop> --host root@<ip>` after reinstalling Debian.

## 2. Re-enrol the node key and deploy

The new instance generated a new `/etc/forge/age.key`; the old
recipient must be replaced (and, if the old node was compromised, removed
from `.sops.yaml` if it was ever added there).

```sh
scripts/deploy init-node <pop>                # overwrites infra/secrets/nodes/<pop>.age.pub
git add infra/secrets/nodes/<pop>.age.pub && git commit -m "secrets: re-enrol <pop>"
grep -n "<pop>" .sops.yaml && { $EDITOR .sops.yaml; scripts/secrets updatekeys infra/secrets/dev.enc.yaml; }   # only if the node was a bundle recipient
scripts/deploy <pop>                          # same TLS cert + host key from the bundle: clients notice nothing
```

Compromised node: also rotate what it held (`rotate-secrets.md`: cluster
secret, its WireGuard key, BGP MD5; `rotate-tls-certificate.md` and
`rotate-ssh-host-key.md` for the shared identities).

## 3a. Repopulate a replica from its leader

```sh
# new node #
sudo -u forge forge admin status              # repos: 0 until the first sync cycle
sleep 30; sudo -u forge forge admin pop status
```

The worker pulls every peer's repository records, events and refs from
scratch. Nothing else to do; `resync-replica.md` if rows stay `error`.

**Cursor caveat**: peers hold `repl_cursors` rows for `<pop>` at the old
node's last event id, and copies of its events keyed by `(node, origin_id)`.
The rebuilt node's event ids restart (at 1 for a fresh replica, at the
backup's last id for a restored node), so peers abort sync from it with
"events at or below cursor" and would silently drop new events whose ids
collide with old ones. Before the new node authors any event (leads a
repository, admin actions), on the rebuilt node read its last local id and
trim every peer to it:

```sh
# rebuilt node #
sudo -u forge sqlite3 /var/lib/forge/forge.db "SELECT COALESCE(MAX(id),0) FROM events WHERE origin_id IS NULL;"   # -> N
# each peer #  (daemon may keep running; short transaction)
sudo -u forge sqlite3 /var/lib/forge/forge.db "DELETE FROM events WHERE node='<pop>' AND origin_id > N; UPDATE repl_cursors SET last_origin_id = N, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE node='<pop>';"
```

Cursor reset and node rebuild tooling is **PLANNED** (`docs/replication.md`
future work); until then this manual step is required whenever a node
name is reused.

## 3b. Repopulate a leader or single node from backup

Follow `restore-from-backup.md` ("single node") on the new instance, then
on every peer run the cursor reset above (the restored node's event log is
shorter than what peers already applied).

## 4. Back into rotation

```sh
scripts/deploy smoke <pop>
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo -u forge forge admin status; sudo -u forge forge admin pop status'
# BGP/WireGuard: network-bootstrap.md (the generated bird.conf/wg0.conf were pushed by deploy; the BGP session needs the node's new provider addresses in netgen --overrides)
scripts/deploy undrain <pop>
```

## Verify

- `/status` is `20` on the per-node name; `forge admin repo check` passes
  for a sample; a clone from `<pop>.nodes.<zone>` matches the leader.
- `bgp-announce status` shows `announced` and exported routes.
- Peers show the node in `pop status` with `ok` rows and no auth errors.

## Rollback

There is no undo for a destroyed instance. If the rebuild fails midway,
keep the POP withdrawn (`scripts/deploy drain`); reads for its
repositories are served by replicas, writes wait.
