# Contributing

## Gate

```sh
make preflight   # gofmt → vet → golangci-lint → build → test -race
```

Always run with `GOWORK=off` (the Makefile does) — a stray `go.work` on
the host breaks builds. Pre-commit hooks run leak-scan + lint; keep them
installed.

## Conventions

- Money: `money.ToMinor` minor units — never floats for prices.
- Errors are handled, not swallowed; write-failures log or bump a metric.
- Tests earn their place by catching failures that would be silent in
  production; no coverage-for-coverage.
- New env config goes through `internal/config` — `config.go` is the
  single env source of truth.
- Stateful features land on Postgres via `internal/postgres` migrations
  (embedded, goose, applied at start). Never add embedded stores.
- The orders package makes no outbound calls; carrier status goes behind
  the `Tracker` seam when implemented.
- Docs, comments, commits in English.

## PRs

Conventional commits (`feat:`/`fix:`/`docs:`/`chore:`) — release-please
cuts versions from them. Squash-merge; release PRs (`chore(main):
release x.y.z`) are merged, not tagged manually.
