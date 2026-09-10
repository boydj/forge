# Handle an abuse or takedown report

**Purpose**: locate reported content, preserve evidence, remove or
restrict it, and record what was done.

**Preconditions**: the report names a Gemini URL, clone URL, or
`owner/repo`; admin CLI on the repository's **leader** node (writes on a
replica are refused/overwritten). Certificates and account actions go to
the metadata leader (`revoke-user-credentials.md`).

**Duration**: 15 min for a repository; longer if evidence must be
collected.

**Triggers**: DMCA/copyright notice, illegal content report, harassment,
malware hosting, spam accounts.

## 1. Map the report to objects

URL layout (`internal/web`, ADR 0009): `gemini://git.<zone>/~<owner>/<repo>/...`
is repository `<owner>/<repo>`; `/~<owner>/<repo>/issues/<n>` an issue;
`/~<owner>` the user's page. Clone URLs: `git@git.<zone>:<owner>/<repo>.git`.

```sh
sudo -u forge forge admin repo list | grep '<owner>/<repo>'       # ID REPO PRIVATE ARCHIVED LEADER SIZE UPDATED
sudo -u forge forge admin user list | grep '<owner>'
ls -la /var/lib/forge/repos/<owner>/<repo>.git
git -C /var/lib/forge/repos/<owner>/<repo>.git log --all --oneline -20
git -C /var/lib/forge/repos/<owner>/<repo>.git ls-tree -r --name-only HEAD | grep -i '<reported path>'
sudo -u forge sqlite3 -header /var/lib/forge/forge.db "SELECT i.number, i.title, u.name author, i.state, i.created_at FROM issues i JOIN repositories r ON r.id=i.repo_id JOIN users u ON u.id=i.author_id JOIN users o ON o.id=r.owner_id WHERE o.name='<owner>' AND r.name='<repo>';"
```

## 2. Preserve evidence (before touching anything)

```sh
sudo install -d -m 0700 /root/evidence/<case>
sudo -u forge git -C /var/lib/forge/repos/<owner>/<repo>.git bundle create /var/lib/forge/tmp/<case>.bundle --all
sudo mv /var/lib/forge/tmp/<case>.bundle /root/evidence/<case>/
sudo -u forge sqlite3 /var/lib/forge/forge.db ".mode json" "SELECT * FROM issues WHERE repo_id=<id>;" "SELECT * FROM comments WHERE repo_id=<id>;" "SELECT * FROM events WHERE repo_id=<id>;" | sudo tee /root/evidence/<case>/metadata.json >/dev/null
journalctl -u forge --since "-30 days" --no-pager | grep -E '(/~<owner>/<repo>|repo=<owner>/<repo>)' | sudo tee /root/evidence/<case>/log.txt >/dev/null
sudo chmod -R go-rwx /root/evidence/<case>
```

Keep the report itself alongside. Evidence lives outside `data_dir` so
backups and maintenance never touch it.

## 3. Act

Pick the least drastic option that satisfies the report.

**Make it read-only (archive)**: refuses pushes, new issues and comments;
content stays visible. No CLI verb; as an administrator open
`gemini://git.<zone>/~<owner>/<repo>/settings/archive` and answer
`archive`, or from the CLI: `forge admin repo archive OWNER/NAME`.

**Hide it (soft delete)**: disappears from every page, feed and clone
immediately; restorable for 7 days, then purged by the nightly
maintenance (or the daemon's hourly loop).

```sh
sudo -u forge forge admin repo delete <owner>/<repo>      # "deleted ...; restorable for 168h0m0s with 'repo restore'"
```

**Remove the bytes now** (legal orders that require immediate removal):
after the soft delete, move the directory out; the later purge tolerates
a missing directory.

```sh
sudo mv /var/lib/forge/repos/<owner>/<repo>.git /root/evidence/<case>/repo.git     # or shred it if retention forbids keeping it
```

Cluster: replicas hold copies. The soft delete replicates as a record
(`deleted_at`) within a sync interval; check
`ls /var/lib/forge/repos/<owner>/` on each replica after the purge and
remove leftovers by hand. **PLANNED**: replica-side purge confirmation.

**Individual issue or comment**: there is no admin CLI to delete one.
Options: archive the repository, disable the author
(`revoke-user-credentials.md`), or, with the daemon running, edit the row
directly and accept that the replicated snapshot from the leader is the
source of truth (do it on the leader): **PLANNED**.

```sh
sudo -u forge sqlite3 /var/lib/forge/forge.db "UPDATE comments SET body='[removed by operator, case <case>]' WHERE id=<comment id>;"
```

**The account**: `forge admin user disable <owner>` (all their content
stays but is unreachable for them); their repositories remain readable
unless also deleted. Spam accounts: disable, then delete each
repository.

## 4. Record and reply

Note in the operations log: case id, report date, URLs, objects (repo id,
issue numbers), action, evidence path, retention date (evidence: keep as
long as the legal exposure, then `shred -u`). Reply to the reporter with
what was removed; if a counter-notice arrives within the 7-day window,
`forge admin repo restore <owner>/<repo>` (and move the directory back
first if you removed it).

## Verify

```sh
printf 'gemini://git.<zone>/~<owner>/<repo>/\r\n' | openssl s_client -quiet -connect 127.0.0.1:1965 -servername git.<zone> 2>/dev/null | head -1   # 51 not found
git ls-remote ssh://git@git.<zone>/<owner>/<repo>.git       # forge: repository 'owner/repo' not found
sudo -u forge forge admin repo list | grep -c '<owner>/<repo>'                 # 0 (soft-deleted rows are hidden)
```

## Rollback

Within 7 days: `forge admin repo restore <owner>/<repo>` (put the
directory back first if moved). After the purge: restore from the
evidence bundle (`git clone --mirror`) or `restore-from-backup.md`.
Archive: `settings/archive` with `unarchive`.
