# Disaster recovery

What a node's state is, how it is backed up, and how to bring it back in
each failure scenario using the commands that exist today. Multi-POP
replication (M5) will add a second line of defence; until then every
repository has exactly one copy on its leader plus the backups described
here.

## State on a node

| Path | What | Recreatable? |
| --- | --- | --- |
| `<data_dir>/forge.db` (+ `-wal`, `-shm`) | all metadata: accounts, certificate and key hashes, repositories, ACLs, issues, comments, events | no: back up |
| `<data_dir>/repos/<owner>/<repo>.git` | bare repositories (content) | no: back up |
| `<data_dir>/assets/` | release assets (planned) | no: back up |
| `<data_dir>/tls/`, `<data_dir>/ssh/` (dev) or `/etc/forge/secrets/` (prod) | service identities | from the SOPS bundle (prod); losing them is a rotation event, see `docs/tls.md` |
| `<data_dir>/tmp/`, `hooks/`, `hook.sock` | scratch, regenerated at start | yes |
| `/etc/forge/forge.toml` | configuration | from `tofu output`/`--config-dir` |

## Backup

**Database.** `forge admin backup --out FILE` runs `VACUUM INTO` on the
live database: an online, transactionally consistent, compacted copy that
needs no downtime (SQLite WAL). The file is a complete database; `-wal`
and `-shm` are not needed.

**Repositories.** Bare git repositories are safe to copy when no push is
in flight; a plain `rsync` during a push can capture refs that point at
objects not yet copied. Use one of:

- `git bundle create <owner>-<repo>.bundle --all` inside each repository
  (one self-contained file per repository, restorable with
  `git clone --mirror`); or
- `git clone --mirror <path> <backup path>` per repository; or
- a filesystem snapshot (LVM/ZFS/btrfs/provider snapshot) of `<data_dir>`
  taken after the database snapshot, then copied at leisure.

Take the database snapshot **first**, then the repositories: a repository
that received a push in between has objects the database does not yet
know about, which is harmless, whereas the reverse could reference a push
whose objects are missing. The `events` table and `repositories.pushed_at`
are the record of what should exist.

**Recommended nightly job** (what `infra/backup/forge-backup` wraps):

```
forge admin backup --config /etc/forge/forge.toml --out /var/backups/forge/forge.db
for r in /var/lib/forge/repos/*/*.git; do
  git -C "$r" bundle create "/var/backups/forge/bundles/$(basename $(dirname $r))-$(basename $r .git).bundle" --all 2>/dev/null || true
done
tar -C /var/backups/forge -cf - forge.db bundles | zstd | age -r "$(cat /etc/forge/secrets/backup.recipient)" -o /var/backups/forge/<stamp>.tar.zst.age
```

`forge-backup` (installed by `scripts/deploy`, run daily by
`forge-backup.timer`) currently calls `forge admin backup --out
<stamp>.tar.zst` expecting a tar of database plus repositories; the CLI
today writes only the SQLite snapshot into that path. Until `admin backup`
grows the repository part, the script's output is a database-only backup
with a misleading extension, and repositories must be bundled separately
as above. The encryption recipient is the `backup_encryption_key` age
identity whose private half stays with the operator (`docs/secrets.md`).
Off-site copy: `OFFSITE_CMD` in `/etc/default/forge-backup`.

Verify a backup by restoring it somewhere else (see the drill).

## Scenarios

### Single node lost (VM gone, disk dead)

Reads and writes for that node's repositories are down until restored
(no failover in v1).

1. Anycast: the prefix is withdrawn automatically when BIRD dies with the
   node; if the node is half-alive, `scripts/deploy drain <pop>` or the
   provider portal.
2. Rebuild: `docs/self-hosting.md` path A or B up to and including
   `scripts/deploy init-node <pop>` (new age key) and `scripts/deploy <pop>`
   (binary, config, secrets: same TLS certificate and host key, so clients
   notice nothing).
3. `systemctl stop forge` on the new node.
4. Restore the database: decrypt the newest archive
   (`age -d -i backup.key <stamp>.tar.zst.age | zstd -d | tar -x`), copy
   `forge.db` to `/var/lib/forge/forge.db`, remove any stale `forge.db-wal`
   / `forge.db-shm`, `chown forge:forge`, mode 0640.
5. Restore repositories: for each bundle `git clone --mirror
   <bundle> /var/lib/forge/repos/<owner>/<repo>.git`, then
   `git -C <repo> symbolic-ref HEAD refs/heads/<default_branch>` (the
   default branch is in `repositories.default_branch`; `forge admin repo
   list` shows the name after the DB is in place) and `chown -R
   forge:forge`. From a filesystem snapshot: copy `repos/` as is.
6. `systemctl start forge`; `forge admin status`; `forge admin repo check
   OWNER/NAME` for every restored repository (or a loop over `repo list`);
   `scripts/deploy smoke <pop>`.
7. Tell users: pushes made after the backup are lost; they push again
   from their clones (git is distributed; their clone is a full copy).
   Issues and comments after the snapshot are lost.

### Database corruption (node up, `forge.db` unreadable or inconsistent)

Symptoms: `forge serve` fails at startup with a migration or open error,
`sqlite3 forge.db 'PRAGMA integrity_check'` is not `ok`, pages return
`40` for everything.

1. `systemctl stop forge`; keep a copy of the broken file and its WAL.
2. Try `sqlite3 broken.db '.recover' | sqlite3 recovered.db`; if
   `integrity_check` on the result is `ok` and `forge admin status
   --config` (pointed at a temporary config with that file) shows sane
   counts, use it.
3. Otherwise restore the newest database snapshot as in the previous
   scenario, step 4. Repositories on disk are newer than the snapshot: run
   `forge admin maintenance` (refreshes sizes) and accept that events
   between snapshot and failure are missing from feeds; pushed refs are
   intact.
4. Start, `admin status`, smoke test.

### Repository corruption (one repository fails `fsck`)

Symptoms: `forge admin repo check OWNER/NAME` reports missing or corrupt
objects; pages for that repository answer `40 repository unavailable`.

1. Move the directory aside (`mv repo.git repo.git.broken`); the row in
   the database stays, so pages show "repository files missing".
2. Restore from the newest bundle: `git clone --mirror <bundle>
   /var/lib/forge/repos/<owner>/<repo>.git`, set `HEAD`, fix ownership.
3. Ask the owner to `git push --mirror` (or push each branch) from an
   up-to-date clone; the pre-receive hook applies as usual. If the
   database still records refs newer than the bundle (events, `pushed_at`)
   the owner's push brings them back.
4. `forge admin repo check`, `repo size`.

### Total loss (all nodes, or the only node, and the operator laptop)

Prerequisites kept off-site: the operator age key (`~/.config/forge/age.key`
backup), the backup age private key, the git repository of this project
(SOPS bundles are committed encrypted), and the newest encrypted backup
archive at the off-site location.

1. New operator machine: install tools (`make tools-infra`), restore the
   age keys, clone the project.
2. Rebuild infrastructure (`docs/self-hosting.md`), deploying the same TLS
   certificate and host key from the bundle so clients keep their pins.
3. Restore database and repositories as in "single node lost".
4. DNS: the anycast records are in OpenTofu; per-node names follow the
   new instances. Re-announce BGP once `/status` is healthy.

If the SOPS bundle or operator key is also lost, the TLS and host keys are
gone: generate new ones, rebuild, and treat it as an unplanned rotation
(`docs/tls.md`, "Key compromise"). Users re-pin; accounts survive because
identities are hashes of the users' own keys.

## RPO / RTO expectations

| | Today (single node, daily backup) | Target after M5 |
| --- | --- | --- |
| RPO metadata | up to 24 h (nightly `forge-backup`); run `forge admin backup` more often if needed, it is cheap | seconds (event log replicated to replicas) |
| RPO repositories | up to 24 h from bundles; effectively zero for anything a contributor still has in a clone | seconds (`git fetch` replication) |
| RTO single node | 1-2 h: rebuild from cloud-init/`deploy`, restore, verify | minutes for reads (other POPs), writes after `repo move-leader` |
| RTO total loss | half a day, dominated by provider provisioning and BGP onboarding | same |

## Restore drill checklist

Run quarterly, and after any change to `forge-backup`, migrations or
`scripts/deploy`. A future `tests/` restore test will automate the first
six steps against a throwaway node.

- [ ] Newest archive decrypts with the off-site backup key (not the copy on
      the node).
- [ ] `sqlite3 forge.db 'PRAGMA integrity_check'` is `ok`;
      `schema_migrations` matches the deployed binary's `migrations/`.
- [ ] Fresh node (`forge admin init` into an empty directory with high
      ports is enough): database copied in, bundles cloned with
      `--mirror`, ownership fixed, `forge serve` starts cleanly.
- [ ] `forge admin status` counts match the source node's last known
      counts; `repo check` passes for every repository.
- [ ] Gemini: front page, one repository overview, a blob, a feed answer
      `20` and match the source.
- [ ] SSH: clone one repository with a registered key; push to a scratch
      branch and delete it.
- [ ] Issues and comments for one repository are present and readable.
- [ ] Time the whole exercise; update the RTO table if it drifted.
- [ ] Record the date and the archive name in the operations log.
