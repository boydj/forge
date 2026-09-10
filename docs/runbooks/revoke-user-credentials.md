# Revoke user credentials

**Purpose**: cut off a compromised client certificate or SSH key, or
disable an account entirely, and find what it did.

**Preconditions**: admin CLI on the right node. In cluster mode users,
certificates and SSH keys are owned by the **metadata leader**
(`cluster.metadata_leader`, default alphabetically first node); a change
made on any other node is overwritten by the next sync (10 s). Run these
commands on the metadata leader.

**Duration**: 2 min; effect is immediate (lookups happen on every
request, nothing is cached).

**Triggers**: user reports a lost laptop / leaked key; abuse
(`handle-abuse-report.md`); suspicious `session`/`request` log lines;
enrolment codes issued during a compromise window (`docs/tls.md`).

## Identify

```sh
sudo -u forge forge admin user list                              # ID NAME ADMIN DISABLED CREATED
sudo -u forge forge admin cert list <user>                       # <spki sha256> active|revoked <label> expires=... last=...
sudo -u forge forge admin key list <user>                        # SHA256:<fp> <type> active|revoked <label>
```

Match a report to a credential: SSH sessions log `account=<user>
key=SHA256:...`; Gemini requests log only `cert=true` (no fingerprint),
so use `last=` from `cert list` and the events table.

## Revoke one credential

```sh
sudo -u forge forge admin cert revoke <spki-sha256>      # next request with that key: 62 certificate revoked
sudo -u forge forge admin key revoke SHA256:<fingerprint> # next SSH auth: Permission denied (publickey)
```

Users can also do this themselves at `/account/certs/revoke/<spki>`
(not the certificate currently in use) and at the account keys page.
Revocation is permanent; there is no un-revoke (re-enrol instead).

## Disable the account

```sh
sudo -u forge forge admin user disable <user>            # Gemini/Titan: 61 account disabled; SSH refused; content stays
sudo -u forge forge admin user admin <user> --revoke     # if they were an administrator
```

`forge admin user enable <user>` reverses it.

## Let the user back in

```sh
sudo -u forge forge admin cert enrol-code <user>         # 16-hex code, valid 15 min, single use
# user: gemini://git.<zone>/account -> "add this certificate to an existing account" -> enter code
sudo -u forge forge admin key add <user> /path/to/new_keys.pub   # or they add it on the account page
```

## Audit what the credential did

```sh
# SSH sessions by that account/key (journal, json or text)
journalctl -u forge --since "-30 days" --no-pager | grep -E 'session.*(account=<user>|key=SHA256:<fp>)'
# events authored by the user (pushes, issues, comments, repo changes), any node
sudo -u forge sqlite3 -header /var/lib/forge/forge.db \
  "SELECT e.id, e.created_at, e.kind, e.subject, e.path, e.node FROM events e JOIN users u ON u.id=e.user_id WHERE u.name='<user>' ORDER BY e.id DESC LIMIT 100;"
# credentials added recently to any account (compromise window)
sudo -u forge sqlite3 -header /var/lib/forge/forge.db \
  "SELECT u.name, c.spki_sha256, c.label, c.created_at, c.last_used_at FROM certificates c JOIN users u ON u.id=c.user_id WHERE c.created_at > '<ISO time>' ORDER BY c.created_at;"
sudo -u forge sqlite3 -header /var/lib/forge/forge.db \
  "SELECT u.name, k.fingerprint, k.label, k.created_at FROM ssh_keys k JOIN users u ON u.id=k.user_id WHERE k.created_at > '<ISO time>';"
```

Reverting pushes: the forge does not rewrite history for you. Ask the
owner to `git push --force` the good state, or restore the repository
from a bundle (`restore-from-backup.md`, repository-level).

## Verify

```sh
sudo -u forge forge admin cert list <user> | grep revoked
sudo -u forge forge admin key list <user> | grep revoked
# cluster: on a replica, after ~10 s the same output (the snapshot replicated)
ssh -i <the revoked key> -T git@git.<zone>              # Permission denied (publickey)
```

## Rollback

Disable is reversible (`user enable`). A revoked certificate or key stays
revoked; issue an enrolment code or `key add` the key again as a new row.
