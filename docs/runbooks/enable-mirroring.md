# Enable mirroring to GitHub

**Purpose**: have the forge push every configured repository to its GitHub
mirror after each push and once an hour, from the repository's leader,
with a dedicated deploy key and pinned host keys (`docs/dogfooding.md`,
"Mirror strategy"). Until the key is in the bundle the feature stays
disabled and the daemon logs one line at start.

**Preconditions**: the bundle and operator age key (`docs/secrets.md`);
write access to the GitHub repository's settings; the POPs deployable
(`upgrade-and-rollback.md`).

**Duration**: 10 min plus one config rollout per POP (~8 min each).
**Triggers**: first setup; key rotation; adding a mirrored repository.

## 1. Generate the deploy key

```sh
# laptop $
umask 077
ssh-keygen -t ed25519 -N "" -C forge-mirror -f /tmp/mirror_ed25519
cat /tmp/mirror_ed25519.pub                 # the PUBLIC key: goes to GitHub
```

On GitHub: repository -> Settings -> Deploy keys -> Add deploy key; paste the
public key; tick **Allow write access**. One key per mirrored repository is
fine (GitHub refuses the same key on two repositories; use a machine user
for many).

## 2. Put the private key in the bundle

```sh
# laptop $
scripts/secrets edit infra/secrets/dev.enc.yaml   # add mirror_deploy_key: | <the private key, indented>
rm -f /tmp/mirror_ed25519 /tmp/mirror_ed25519.pub
git add infra/secrets/dev.enc.yaml && git commit -m "secrets: mirror deploy key"
```

The key travels with the node's secret subset (`scripts/deploy` ->
`/etc/forge/secrets.tar.age` -> `/run/forge/secrets/mirror.key`, tmpfs,
0400 forge). It is never written to a node's root filesystem.

## 3. Configure the mirrors

`infra/opentofu/environments/dev/variables.tf` (`mirrors`, default
`jdb/forge` -> `git@github.com:boydj/forge.git`) or `dev.auto.tfvars`
renders `[[mirrors]]` into every POP's `forge.toml`; only the repository's
leader pushes. GitHub's host keys are pinned in `infra/mirror/known_hosts`
(installed as `/etc/forge/mirror_known_hosts`); refresh that file from
`https://api.github.com/meta` when GitHub rotates them.

```sh
# laptop $
eval "$(scripts/secrets env infra/secrets/dev.enc.yaml)"
tofu -chdir=infra/opentofu/environments/dev plan          # outputs only: forge.toml gains [mirror]/[[mirrors]]
tofu -chdir=infra/opentofu/environments/dev apply
```

## 4. Roll out, one POP at a time

`scripts/deploy <pop>` ships the key (secrets), the pinned host keys and
the new `forge.toml`, and restarts the daemon; drain first as in
`upgrade-and-rollback.md`. The daemon logs `mirroring enabled` (or
`mirroring disabled` with the reason).

## 5. Verify

```sh
# laptop $
ssh -p 2200 root@<leader>.nodes.<zone> 'sudo -u forge forge admin repo mirror jdb/forge'
#   -> mirrored jdb/forge to git@github.com:boydj/forge.git in 1.2s
git ls-remote git@github.com:boydj/forge.git main        # matches the forge's main
```

On the monitoring host: `forge_mirror_pushes_total{result="ok"}` rises,
`forge_mirror_last_success_seconds{repo="jdb/forge"}` is recent, and
`MirrorStale` is not firing. A failing push is retried after 1, 5 and then
every 30 minutes; the error is in the daemon's journal (`mirror push
failed`) and in the output of `repo mirror`.

## Rotate the key

Repeat step 1 and 2 with a new key, remove the old deploy key on GitHub,
and redeploy each POP (`scripts/deploy <pop>`): the next reconcile uses the
new key.

## Rollback

Set `mirrors = {}` (or remove the entry) and redeploy: the daemon stops
mirroring at the next restart. Nothing is pulled from GitHub, ever.
