# Enable off-site backups (Backblaze B2)

**Purpose**: copy every nightly `forge-backup` archive from each POP to a
Backblaze B2 bucket with a write-only key, so losing a node (or all of
them) does not lose the backups. Design in `docs/disaster-recovery.md`,
"Off-site"; code in `infra/opentofu/environments/b2`,
`infra/backup/forge-offsite`, `scripts/deploy`.

**Preconditions**: a Backblaze account; the POPs on a build that has
`rclone` and `forge-offsite` (`scripts/deploy sysupdate <pop>` installs
both on existing nodes). **Duration**: 15 min. **Cost**: B2 storage is
$6/TB-month with the first 10 GB free; three POPs at ~1 MB/night, 30-day
retention, is well inside the free tier (`docs/costs.md`).

## 1. Operator: an admin application key (once)

In the Backblaze console (Account, Application Keys), *Add a New
Application Key*: name `forge-tofu`, access to all buckets, all
capabilities. Put the two values in the bundle:

```sh
# laptop $
scripts/secrets edit infra/secrets/dev.enc.yaml    # b2_admin_key_id, b2_admin_key
```

This key is used only by OpenTofu on the laptop; it never reaches a node.

## 2. Create the bucket and the writer key (OpenTofu)

```sh
eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"     # exports B2_APPLICATION_KEY_ID / B2_APPLICATION_KEY
tofu -chdir=infra/opentofu/environments/b2 init
tofu -chdir=infra/opentofu/environments/b2 apply              # bucket (private, SSE-B2, 30-day version lifecycle) + writer key
tofu -chdir=infra/opentofu/environments/b2 output writer_key_id
tofu -chdir=infra/opentofu/environments/b2 output -raw writer_key
```

Put `writer_key_id` / `writer_key` in the bundle as `b2_backup_key_id` /
`b2_backup_key` (`scripts/secrets edit`, or `sops set`). The writer key can
list and write in that bucket only: it cannot read other archives or
delete anything; retention is the bucket's lifecycle rule.

## 3. Turn it on per environment

Add to `infra/opentofu/environments/dev/dev.auto.tfvars`:

```hcl
offsite_bucket = "as215520-forge-backups"
```

Then, one POP at a time (a config-only deploy restarts forge for a few
seconds; the full drain sequence is in `upgrade-and-rollback.md`):

```sh
tofu -chdir=infra/opentofu/environments/dev apply             # outputs only: forge_backup_env
scripts/deploy sysupdate <pop>                                 # rclone, forge-offsite, the updated helper and unit
scripts/deploy <pop>                                           # ships rclone.conf (tmpfs) and /etc/default/forge-backup
```

## 4. Verify

```sh
ssh -p 2200 root@<pop>.nodes.<zone> 'systemctl start forge-backup.service; journalctl -u forge-backup -n 5 --no-pager'
#   ... forge-offsite: copied <stamp>.tar.gz.age to b2:<bucket>/<pop>/
rclone --config <(printf '[b2]\ntype = b2\naccount = %s\nkey = %s\n' "$B2_APPLICATION_KEY_ID" "$B2_APPLICATION_KEY") ls b2:<bucket>/<pop>/
```

A failed copy fails `forge-backup.service`, which `NodeSystemdUnitFailed`
reports on the alerts feed. Restoring from B2: `restore-from-backup.md`,
step 0.

## Rollback

Remove `offsite_bucket` from the tfvars, apply, deploy; or empty
`/etc/default/forge-backup` on the node. The bucket and its contents stay.
