# Enable off-site backups (Backblaze B2)

**Purpose**: copy every nightly `forge-backup` archive from each POP to a
Backblaze B2 bucket with a write-only key, so losing a node (or all of
them) does not lose the backups. Design in `docs/disaster-recovery.md`,
"Off-site"; desired bucket state in `infra/backup/b2.yaml`; code in
`infra/backup/forge-offsite`, `scripts/deploy`, `scripts/b2check`.

No account-wide Backblaze key is created or stored anywhere: the bucket
and two bucket-restricted keys are made by hand in the console (a
restricted key cannot create buckets), and the repository verifies the
result.

**Preconditions**: a Backblaze account; the POPs on a build with `rclone`
and `forge-offsite` (`scripts/deploy sysupdate <pop>` installs both).
**Duration**: 15 min. **Cost**: $6/TB-month, first 10 GB free; three POPs
at ~1 MB/night with 30-day retention is inside the free tier.

## 1. Console: the bucket (once)

Buckets, *Create a Bucket*: name `as215520-forge-backups`, **Private**,
default encryption **SSE-B2** enabled, object lock off. Then *Lifecycle
Settings* on the bucket: custom rule, all files (empty prefix), *Days Till
Hide* 30, *Days Till Delete* 1. That rule is the retention; nothing else
deletes.

## 2. Console: two keys restricted to that bucket

Application Keys, *Add a New Application Key*, both with *Allow access to
Bucket(s)* = `as215520-forge-backups` only:

| Name | Type of Access | Capabilities that result | Goes to |
| --- | --- | --- | --- |
| `forge-backup-writer` | **Write Only** | `listBuckets, listFiles, writeFiles` (no read, no delete) | every node, as `/run/forge/secrets/rclone.conf` |
| `forge-backup-reader` | **Read Only** | `listBuckets, listFiles, readFiles` | your laptop only, for restores and drills |

Put the four values in the bundle:

```sh
# laptop $
scripts/secrets edit infra/secrets/dev.enc.yaml
#   b2_backup_key_id / b2_backup_key      (writer)
#   b2_restore_key_id / b2_restore_key    (reader)
scripts/b2check                                # bucket name, type, lifecycle, encryption, writer capabilities: all "ok"
```

## 3. Turn it on

Add `offsite_bucket = "as215520-forge-backups"` to
`infra/opentofu/environments/dev/dev.auto.tfvars`, then one POP at a time
(a config deploy restarts forge for a few seconds; the full drain sequence
is `upgrade-and-rollback.md`):

```sh
tofu -chdir=infra/opentofu/environments/dev apply             # outputs only: forge_backup_env
scripts/deploy <pop>                                           # ships rclone.conf (tmpfs) and /etc/default/forge-backup
```

## 4. Verify

```sh
ssh -p 2200 root@<pop>.nodes.<zone> 'systemctl start forge-backup.service; journalctl -u forge-backup -n 5 --no-pager'
#   ... forge-offsite: copied <stamp>.tar.gz.age to b2:as215520-forge-backups/<pop>/
eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"      # reader key as B2_APPLICATION_KEY_ID / B2_APPLICATION_KEY
rclone --config <(printf '[b2]\ntype = b2\naccount = %s\nkey = %s\n' "$B2_APPLICATION_KEY_ID" "$B2_APPLICATION_KEY") ls b2:as215520-forge-backups/<pop>/
```

A failed copy fails `forge-backup.service`, which `NodeSystemdUnitFailed`
reports on the alerts feed. Restoring from B2: `restore-from-backup.md`,
step 0. Run `scripts/b2check` after any console change.

## Rollback

Remove `offsite_bucket` from the tfvars, apply, deploy; or empty
`/etc/default/forge-backup` on the node. The bucket and its contents stay.
