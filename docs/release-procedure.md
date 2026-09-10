# Release procedure

Releases are git tags plus static binaries. Nothing about a release lives
outside the repository except the published artefacts.

## Versioning

Semantic versions `vMAJOR.MINOR.PATCH`. Pre-1.0: MINOR bumps may change
configuration keys or URL layout; every such change is called out in
`CHANGELOG.md` with the migration step. The database schema migrates
forward automatically at daemon start (`migrations/`); downgrades are not
supported, so back up before upgrading (`forge admin backup`).

## Steps

1. `make check` on a clean tree (lint, unit, integration).
2. Update `CHANGELOG.md` (Added / Changed / Fixed / Operations).
3. Tag: `git tag -a vX.Y.Z -m "forge vX.Y.Z"`.
4. Build: `scripts/release vX.Y.Z` produces `dist/vX.Y.Z/` with
   `forge_vX.Y.Z_linux_{amd64,arm64}`, a source tarball and `SHA256SUMS`.
5. Sign `SHA256SUMS` with the maintainer's SSH key so users can verify
   with the key they already trust for the forge:
   `ssh-keygen -Y sign -f ~/.ssh/id_ed25519 -n file dist/vX.Y.Z/SHA256SUMS`.
6. Publish: push the tag; attach the artefacts as a release on the forge
   itself once dogfooding is enabled (`docs/dogfooding.md`), via Titan asset
   upload to `/~forge/forge/releases/vX.Y.Z`; until then attach them to the
   GitHub mirror release.
7. Deploy: `scripts/deploy <pop> --binary dist/vX.Y.Z/forge_vX.Y.Z_linux_amd64`
   one POP at a time, draining first (`docs/runbooks/upgrade-and-rollback.md`).

## Verifying a download

```
sha256sum -c SHA256SUMS
ssh-keygen -Y verify -f allowed_signers -I maintainer -n file -s SHA256SUMS.sig < SHA256SUMS
```

`allowed_signers` contains the maintainer key as published at
`gemini://git.as215520.net/~forge/` and in `SECURITY.md` once the forge is live.
