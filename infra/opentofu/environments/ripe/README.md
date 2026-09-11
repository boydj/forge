# RIPE Database objects as code

Manages `as-set AS215520:AS-ALL` and `aut-num AS215520` with the
`frederic-arr/ripedb` provider. The attribute lists are parsed from
`infra/network/irr/*.rpsl`, so edit those files, never this directory.

```
eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"   # exports TF_VAR_ripe_api_key when ripe_db_api_key is in the bundle
tofu -chdir=infra/opentofu/environments/ripe init
tofu -chdir=infra/opentofu/environments/ripe import ripedb_object.aut_num "aut-num:AS215520"   # once: the aut-num already exists
tofu -chdir=infra/opentofu/environments/ripe plan                     # provider dry_run=true: RIPE DB validates, nothing written
tofu -chdir=infra/opentofu/environments/ripe apply -var dry_run=false # writes both objects
scripts/netcheck                                                     # irr rows turn OK
```

Notes:

- `plan` with `dry_run = true` (the default) still talks to the RIPE DB with
  the key, so authorisation and syntax errors surface before anything is
  written.
- Deleting a resource deletes the object in the RIPE DB. To stop managing
  without deleting, `tofu state rm`.
- The key expires within a year (RIPE policy); refresh it in the bundle.
- Other IRR objects (route6, inetnum) are RIPE NCC or LIR managed and stay
  out of scope; `scripts/netcheck` verifies them read-only.
