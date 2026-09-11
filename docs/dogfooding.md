# Dogfooding: hosting the forge on the forge

The forge's own repository should live on the forge. Doing it too early
turns every outage into a development outage and hides bugs behind
workarounds only the maintainers know; doing it well is the best
acceptance test there is. This page fixes when and how.

## When

After **M5 (replication and leadership) is merged and backups are
verified**, specifically once all of the following are true:

1. At least two POPs serve reads of the same repositories, and
   `forge admin repo move-leader` has been exercised for real.
2. `forge-backup` produces database plus repository archives and the
   restore drill in `docs/disaster-recovery.md` has passed on a throwaway
   node from a production archive.
3. `make check` has been green on `main` for the M3 and M5 acceptance
   tests for at least two weeks of ordinary use (issues, reviews, pushes)
   by the maintainers on a non-critical repository.
4. The service TLS certificate and SSH host key in production are the
   long-lived ones from the SOPS bundle (no rotation pending), SSHFP
   records are published, and DNSSEC is enabled for the zone.

Until then the forge is developed on GitHub and deployed from a laptop.

## Bootstrap order

1. Create the maintainer accounts on the production forge (client
   certificate registration, SSH keys enrolled through `/account/keys`).
2. `forge admin repo create <maintainer>/forge --description ...` on the
   leader POP; push the full history including tags:
   `git push --mirror ssh://git@git.as215520.net/<maintainer>/forge.git`
   (the pre-receive hook accepts `refs/heads/*`, `refs/tags/*`,
   `refs/notes/*`; anything else, such as GitHub pull-request refs, must
   be excluded from the mirror push).
3. Add collaborators with `write`; keep `admin` for the owner.
4. Point the checkout used for deploys at the forge as `origin` and keep
   GitHub as `github`.
5. Re-create open issues by hand (there is no import); link the GitHub
   issue number in the body. Close the GitHub issue tracker to new issues
   with a pointer to the Gemini URL.
6. Update `README.md`, `CONTRIBUTING.md` and `SECURITY.md` to name the
   forge as canonical and GitHub as a mirror.

## Mirror strategy

- **GitHub stays as a read-only mirror** until the forge has served the
  repository for a full quarter without a restore, a lost push or an
  unplanned identity rotation. Mirroring is a post-receive job on the
  leader (planned `forge admin repo mirror` or a cron `git push --mirror
  github` from a deploy-user clone), never the other way round.
- Once reliability is proven the GitHub repository becomes an archived
  mirror with a README pointing at the forge, and CI (below) still runs
  there from the mirror if convenient.
- Release assets: uploaded to the forge (M3 releases) and copied to the
  GitHub release page by the same mirror job while the mirror exists.

## Avoiding the circular dependency

The forge must be buildable, testable and deployable with the forge down.

- **CI uses only `make` targets and `scripts/*`**
  (`.github/workflows/ci.yml` already does): no forge-specific CI features,
  no dependency on the forge's own API, feeds or Titan endpoints. The same
  commands run on a laptop.
- **Deploys come from a local checkout**: `scripts/deploy` builds from the
  working tree and pushes over SSH to the node's admin port; it does not
  fetch from the forge. A broken forge cannot block its own fix.
- **Secrets and infrastructure state** stay in the SOPS bundles and
  OpenTofu state, never inside the forge.
- **Two copies of history always exist** outside the forge: the mirror and
  every maintainer's clone. `git` is distributed; the forge is a
  coordination point, not the only copy.
- **Backups are pulled off-node** by the operator (`OFFSITE_CMD`), so
  restoring the forge never requires the forge.
- The restore drill is run against the forge's own repository first: if the
  forge can rebuild itself from an archive, it can rebuild anything.

## What would make us pull back

Any of: a lost push, a restore that needed manual object surgery, an
identity rotation forced by a node compromise, or two unplanned outages of
the leader in a month. In that case `git push --mirror github` from a
clone makes GitHub canonical again within minutes; that is why the mirror
is kept warm.

## The documentation site

`gemini://git.as215520.net/docs/` is `docs/site/` of this repository, read
straight from `jdb/forge` at its default branch (`docs.repo` and
`docs.path` in `forge.toml`, rendered by `modules/forge-node`). It is the
**user guide**: how to get an account, clone and push, open issues, propose
changes, subscribe to feeds, and what to pin. The rest of `docs/` (this
file, architecture, operations, runbooks, decisions) is for people working
on the forge and is not published. Incident reports are
`docs/site/incidents/`, which the status page and its feeds read.

There is no build step and no second copy: a git push that changes a file
under `docs/site/` is a docs deploy, replicated to every POP with the
repository. Markdown renders with relative links rebased, so the same
files read correctly on GitHub, in a clone and on the site; the index is
`docs/site/README.md`. Only a public repository can be the documentation
source, whoever is asking.
