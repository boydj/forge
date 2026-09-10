# Upgrade and rollback

**Purpose**: ship a new `forge` binary, units, configs and secrets to a POP
with `scripts/deploy`, and revert to the previous binary (`forge.prev`) if
the new one misbehaves.

**Preconditions**: `main` builds and `make check` passes; the node is
reachable on 2200; for an anycast POP, another POP is announcing (check
with `ForgeNoAnnouncers` logic: `bgp-announce status` elsewhere).

**Duration**: 3-5 min per POP plus ~130 s out of rotation after the restart
when the health worker controls BGP (startup cooldown, `docs/health.md`).

**Triggers**: release; hotfix; config change (`tofu` or `--config-dir`);
secret rotation (`rotate-secrets.md`).

## Before you start

```sh
# laptop $
git log --oneline -1
ls migrations/                       # any new migration since the deployed version?
scripts/deploy <pop> --dry-run       # prints every ssh/scp the deploy will run
```

Migrations are additive and run at daemon start. Rolling back **across** a
migration is only safe if the old binary does not read columns the new
migration changed; read the new file before you rely on rollback.

## Upgrade one POP

```sh
# 1. take it out of rotation (anycast POPs only; see drain-pop.md for detail)
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo -u forge forge admin pop drain'   # pins the health controller
sleep 300                                                                       # let paths move (RFC 8326 drain)
scripts/deploy drain <pop>                                                      # bgp-announce withdraw on the node

# 2. deploy: build, install binary (previous kept as /usr/local/bin/forge.prev), units, configs, secrets, restart, smoke
scripts/deploy <pop>
#   options: --binary bin/forge (skip the build), --no-secrets, --config-dir DIR (instead of tofu output)

# 3. check, then back into rotation
ssh -p 2200 deploy@<pop>.nodes.<zone> 'journalctl -u forge -n 40 --no-pager; sudo -u forge forge admin status; forge version'
scripts/deploy undrain <pop>                                                    # bgp-announce announce
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo -u forge forge admin pop undrain'   # release the pin
```

Repeat per POP, one at a time. Do **not** deploy a TLS/host-key change
this way: those must land on all POPs in one window
(`rotate-tls-certificate.md`).

## Verify

```sh
scripts/deploy smoke <pop>
ssh -p 2200 deploy@<pop>.nodes.<zone> 'systemctl is-active forge; journalctl -u forge --since "-5 min" -p err --no-pager'
ssh -p 2200 deploy@<pop>.nodes.<zone> 'sudo bgp-announce status'              # anycast POPs: state announced, routes exported
```

The deploy itself fails if `systemctl is-active forge` is false 2 s after
the restart (it prints the last 30 journal lines) or if the smoke test
fails. A failed deploy leaves the new binary installed: roll back.

## Rollback

```sh
scripts/deploy rollback <pop>      # mv forge -> forge.failed, forge.prev -> forge, restart, smoke
```

Only one generation is kept: a second rollback needs a rebuilt binary from
the wanted commit: `git checkout <tag> && scripts/deploy <pop>`, or
`go build` it once and `scripts/deploy <pop> --binary <file>`.

Configs and secrets are not versioned on the node. To revert a config, fix
the source (`tofu` inputs or `--config-dir`) and deploy again; `deploy`
without a binary change is fine (`--binary` with the current file, or just
let it rebuild).

## If the daemon will not start

```sh
ssh -p 2200 deploy@<pop>.nodes.<zone> 'journalctl -u forge -n 100 --no-pager'
# config error: "config /etc/forge/forge.toml: unknown keys" -> fix the template / config-dir, redeploy
# migration error -> rollback is NOT safe past that point; see restore-from-backup.md, keep the db copy
# listen error on 22/1965 -> another process bound it: ss -ltnp | grep -E ':22 |:1965 '
```
