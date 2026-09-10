# Restore from backup

**Purpose**: put a node's database and repositories back from a
`forge-backup` archive, on one node (disk/database loss) or after total
loss. Uses `forge admin restore`.

**Preconditions**

- An archive `/var/backups/forge/<stamp>.tar.gz.age` (kept: newest 7 on
  the node; off-site copy only if `OFFSITE_CMD` is set in
  `/etc/default/forge-backup`, which `deploy` does not install: **PLANNED**).
- The backup private key: `backup_encryption_key` in the SOPS bundle
  (and its offline copy). The key is never on the node.
- A node with the binary and config in place (`deploy-first-node.md` or
  `rebuild-pop.md`), daemon stopped for the restore step.

**Duration**: 15-60 min for a single node depending on repository size;
half a day for total loss (provisioning dominates).

**Triggers**: database corruption, repository corruption not fixable by
the owner's re-push, VM lost, total loss, quarterly restore drill.

## What the archive contains

`forge admin backup --out FILE.tar.gz` writes: `forge.db` (SQLite
`VACUUM INTO` snapshot, taken first), `repos/<owner>/<name>.bundle`
(`git bundle create --all`, one per non-empty repository), and the
`assets/`, `tls/`, `ssh/` subtrees of the data directory (empty on
production nodes, whose identities live in `/run/forge/secrets`).
`forge-backup` wraps it with `age -r <backup recipient>` and names it
`<stamp>.tar.gz.age`. (`docs/disaster-recovery.md` still says
`.tar.zst.age`; the script writes `.tar.gz.age`.) `FILE.db` gives a
database-only snapshot.

## 0. Get and decrypt the archive (laptop)

```sh
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo ls -l /var/backups/forge/'
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo install -m 0600 -o deploy /var/backups/forge/<stamp>.tar.gz.age ~/'   # /var/backups/forge is root 0700
scp -P 2200 deploy@<pop>.nodes.<zone>:<stamp>.tar.gz.age . && ssh -p 2200 deploy@<pop>.nodes.<zone> 'rm ~/<stamp>.tar.gz.age'
umask 077; scripts/secrets get infra/secrets/dev.enc.yaml backup_encryption_key > backup.key
age -d -i backup.key -o <stamp>.tar.gz <stamp>.tar.gz.age
tar -tzf <stamp>.tar.gz | head                                            # forge.db, repos/..., ...
tar -xzOf <stamp>.tar.gz forge.db > check.db && sqlite3 check.db 'PRAGMA integrity_check; SELECT MAX(version) FROM schema_migrations; SELECT COUNT(*) FROM repositories WHERE deleted_at IS NULL;'
rm backup.key check.db
```

Keep the private key off the node: decrypt on the laptop, ship the plain
`tar.gz` over SSH.

## 1. Single node: restore in place

```sh
scp -P 2200 <stamp>.tar.gz deploy@<pop>.nodes.<zone>:restore.tar.gz
ssh -p 2200 deploy@<pop>.nodes.<zone>
# node #
sudo -u forge forge admin pop drain; sudo bgp-announce withdraw 2>/dev/null   # anycast POPs
sudo systemctl stop forge
sudo -u forge mkdir -p /var/lib/forge/keep && sudo -u forge cp -a /var/lib/forge/forge.db* /var/lib/forge/keep/ 2>/dev/null   # keep the broken copy
sudo install -m 0640 -o forge -g forge restore.tar.gz /var/lib/forge/tmp/restore.tar.gz
sudo -u forge forge admin restore --in /var/lib/forge/tmp/restore.tar.gz
#   refuses with "hook socket exists" if the daemon is running.
#   forge.db replaces the database (stale -wal/-shm removed); each bundle is fetched into a fresh bare repo
#   (existing directories with the same name are REPLACED); repositories that were empty at backup time get empty dirs.
sudo rm -f /var/lib/forge/tmp/restore.tar.gz ~/restore.tar.gz
sudo systemctl start forge
```

Repositories on disk that are newer than the snapshot are overwritten by
the bundle: pushes after the backup are lost and must be re-pushed by
their owners from their clones. If only the **database** is broken and the
repositories are fine, restore the database only:

```sh
sudo systemctl stop forge
sudo -u forge sh -c 'cd /var/lib/forge && tar -xzOf tmp/restore.tar.gz forge.db > forge.db.new && rm -f forge.db-wal forge.db-shm && mv forge.db.new forge.db'
sudo systemctl start forge
sudo -u forge forge admin maintenance        # refresh sizes; pushed refs are intact, events after the snapshot are missing
```

## 2. Total loss

1. New laptop: `make tools-infra`, restore `~/.config/forge/age.key` and
   the backup key from the offline copies, clone the project.
2. `deploy-first-node.md` end to end (same TLS certificate and host key
   from the bundle, so clients keep their pins). If the bundle or operator
   key is also gone, generate new identities and treat it as an unplanned
   `rotate-tls-certificate.md` + `rotate-ssh-host-key.md`.
3. Step 1 above on the new node with the newest off-site archive.
4. `network-bootstrap.md` to announce again; cluster nodes:
   `rebuild-pop.md` step 3 (cursor reset on peers) if any survived.

## Verify

```sh
sudo -u forge forge admin status                                # counts match the pre-incident numbers
sudo -u forge forge admin repo list | awk 'NR>1{print $2}' | while read -r r; do sudo -u forge forge admin repo check "$r" >/dev/null || echo "BAD $r"; done
sudo -u forge forge admin maintenance --check                   # gc + fsck + sizes; non-zero exit lists corrupt count
journalctl -u forge -n 30 --no-pager
scripts/deploy smoke <pop>                                      # laptop
# a user: clone one repo, read one issue, push to a scratch branch
```

Then release: `sudo bgp-announce announce; sudo -u forge forge admin pop undrain`.

Tell users which time window of pushes/issues is lost (`stamp` of the
archive).

## Rollback

The pre-restore database is under `/var/lib/forge/keep/`; repository
directories replaced by bundles are gone (git has no trash). If the
restore was wrong, stop the daemon, move the kept db back, and restore
from a different archive. Delete `keep/` once satisfied.

## Drill (quarterly)

Run steps 0 and 1 against a scratch directory on the laptop instead of a
node: `forge admin init --data /tmp/drill --hostname localhost
--gemini-listen 127.0.0.1:19650 --ssh-listen 127.0.0.1:22000
--write-config /tmp/drill/forge.toml`, then
`forge admin --config /tmp/drill/forge.toml restore --in <stamp>.tar.gz`,
`forge serve --config /tmp/drill/forge.toml`, and walk the checklist in
`docs/disaster-recovery.md`. Record date, archive name and elapsed time.
