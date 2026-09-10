# Disk full

**Purpose**: recover a node whose free space fell below
`limits.min_free_bytes` (1 GiB): `/status` answers `41 unhealthy: disk`,
the health worker drains then withdraws the POP (when wired), repository
creation and pushes are refused (`pre-receive`), and SQLite may fail to
checkpoint.

**Preconditions**: operator SSH on 2200 (sshd still works with a full
disk; `sudo` too).

**Duration**: 10-30 min.

**Triggers**: `ForgeUnhealthy` with `disk` in the `/status` detail,
`node_filesystem_avail_bytes{mountpoint="/"}` < 2 GiB
(`forge_disk_free_bytes` is registered but **not populated**, use node
exporter: **PLANNED**), user reports "server busy"/"insufficient disk".

## 1. Confirm and take the POP out

```sh
ssh -p 2200 deploy@<pop>.nodes.<zone>
df -h / /var/lib/forge /var/backups
sudo -u forge forge admin status | grep 'disk free'
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername git.<zone> 2>/dev/null | head -1   # 41 unhealthy: disk
sudo -u forge forge admin pop drain; sudo bgp-announce withdraw 2>/dev/null     # anycast: stop attracting traffic (drain-pop.md)
```

Note: the `drain` marker itself is a small file in `data_dir`; if even
that fails, free space with step 2 first.

## 2. Find what grew

```sh
sudo du -xsh /var/lib/forge/* /var/backups/forge /var/log /var/cache/apt /tmp 2>/dev/null | sort -h
sudo du -xsh /var/lib/forge/repos/*/*.git | sort -h | tail -15
sudo -u forge forge admin repo list | awk 'NR>1{print $6, $2}' | sort -n | tail -10   # SIZE REPO (may be stale; `repo size` refreshes)
sudo ls -la /var/lib/forge/tmp/                                          # stuck uploads, purge-* staging, backup snapshots/bundles
sudo ls -l /var/lib/forge/forge.db*                                      # a huge -wal means checkpoints are failing
sudo journalctl --disk-usage
```

## 3. Free space, safest first

```sh
sudo journalctl --vacuum-size=200M
sudo apt-get clean
sudo find /var/lib/forge/tmp -maxdepth 1 -type f -mmin +120 -print -delete      # abandoned Titan uploads, leftover snapshot-*.db / bundle-*.bundle
sudo find /var/lib/forge/tmp -maxdepth 1 -name 'purge-*' -mmin +120 -print -exec rm -rf {} +
sudo ls -1t /var/backups/forge/*.tar.gz.age | tail -n +3 | sudo xargs -r rm -v   # keep the newest 2 on-node (they are 7 by default)
sudo -u forge forge admin maintenance      # purge repos deleted >7 days ago, git gc --auto, size refresh, PRAGMA optimize + wal_checkpoint(TRUNCATE)
df -h /var/lib/forge
```

A single oversized repository (quota is 2 GiB per repository, 10 GiB per
owner; admins exempt):

```sh
sudo -u forge git -C /var/lib/forge/repos/<owner>/<repo>.git count-objects -vH
sudo -u forge git -C /var/lib/forge/repos/<owner>/<repo>.git gc --prune=now      # repack; gc.auto is 0 in the daemon env, so this is manual
sudo -u forge forge admin repo size <owner>/<repo>
```

Abuse or a runaway user: `handle-abuse-report.md` (soft delete moves the
directory only at purge time; move it out by hand to reclaim space now).

## 4. Grow the disk (if the data is legitimate)

The Vultr plan is `vc2-1c-1gb` (25 GB). Options: `vultr_plan` upgrade in
`dev.auto.tfvars` + `tofu apply` (instance resize, reboot), or attach block
storage and move `/var/lib/forge` to it. Neither is in the OpenTofu
modules yet: **PLANNED**. Resize by hand in the console and `resize2fs`
after the reboot, or `rebuild-pop.md` onto a larger plan with
`restore-from-backup.md`.

## 5. Back into service

```sh
sudo -u forge forge admin status
printf 'gemini://git.<zone>/status\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername git.<zone> 2>/dev/null | head -1   # 20 ...
sudo bgp-announce announce; sudo -u forge forge admin pop undrain
```

## Verify

`df -h` shows well above 1 GiB plus headroom for one day of growth;
`/status` is `20`; a test push succeeds; `forge_healthy 1` on `/metrics`;
`forge-backup.timer` next run will have room (an archive is roughly the
size of `repos/` + `forge.db`, compressed, and is built in
`/var/backups/forge/.tmp.*` before encryption, so keep **2x the data size
free** or backups fail silently in the journal:
`journalctl -u forge-backup`).

## Rollback

Nothing destructive above except deleting old backups and tmp files. If
you removed the wrong backup, the off-site copy (if configured) is the
only other source.
