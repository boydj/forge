# Contributing

Thanks for your interest. This project aims to be boring where it can be and
interesting only where that buys something. Contributions that add simplicity are
as welcome as contributions that add features.

## Ground rules

- Everything practical is code: configuration, infrastructure, tests, runbooks.
- No HTTP/HTML/JavaScript in the user-facing product. Gemini, Titan, SSH, Git, DNS, BGP.
- Keep the binary count small. Prefer a subcommand over a new daemon.
- Security invariants in `docs/threat-model.md` are not negotiable without an ADR.
- Significant decisions are recorded in `docs/decisions/` as ADRs.

## Development

```
make dev          # install pinned tools into ./bin and $GOPATH/bin
make build        # build ./bin/forge
make test         # unit tests with race detector
make integration  # end-to-end tests (git, ssh, gemini, titan)
make lint         # gofmt, go vet, staticcheck, govulncheck, tofu fmt/validate
make run          # local forge with a throwaway data dir
```

See `docs/development.md` for details.

## Commits

Use conventional prefixes: `feat:`, `fix:`, `docs:`, `test:`, `infra:`, `net:`, `ops:`, `chore:`.
Keep commits to one logical change.

## Reporting security issues

See `SECURITY.md`.

## License

By contributing you agree that your contributions are licensed under Apache-2.0.
