# CLAUDE.md

Notes for coding agents working in quarryn: a Go service (MCP server plus REST bridge) for
agent-native product search, price watches and order tracking. Every document is indexed in
[docs/INDEX.md](docs/INDEX.md); read [ARCHITECTURE.md](ARCHITECTURE.md) for the package map and
request path before changing the pipeline.

## Layout

- `cmd/server/main.go`: wiring (config, Postgres, adapters, mux, watch checker).
- `internal/sources` (adapters, funnel, host manifests, offer ids), `extract`, `match`, `rank`,
  `search` (the funnel entrypoint), `group` (product identity), `watch`, `orders`, `trust`,
  `probe`, `api` (MCP tools and REST twins), `config`, `auth`, `money`, `postgres` (embedded
  goose migrations).
- `vendor/` is committed upstream code. Never edit or reformat it.
- Environment variables are defined only in `internal/config/config.go`; the table is in
  [docs/OPERATIONS.md](docs/OPERATIONS.md).

## Gate

CI (`.github/workflows/preflight.yml`) runs `make preflight`, and so should you:

```sh
make preflight   # gofmt (vendor excluded) -> go vet -> golangci-lint -> go build -> go test -race -count=1
```

Everything runs with `GOWORK=off`; the Makefile sets it. Go is the 1.26 line (do not move CI to a
newer minor until the linter supports it). A docs-only PR (Markdown, `docs/`, licence, issue and PR
templates) skips the job through the `docs-scope` job in the same workflow; that block is
canonical across the fleet and compared byte for byte, so do not edit it here.

## Invariants that are easy to break

- **Hard criteria fail closed in code.** `price_max`, `condition`, `availability`, `not_keyword`,
  `brand` and `currency` are enforced by the prefilter, never by a prompt. A missing LLM only
  degrades ranking and must be flagged (`degraded` plus a reason).
- **Money is minor units** (`money.ToMinor`), never a float.
- **Every route needs the bearer secret except `GET /healthz`;** an empty
  `INTERNAL_SERVICE_SECRET` makes every route answer 401.
- **All third-party page egress goes through the configured fetch endpoint,** except the
  first-party API adapters. Candidate URLs get the SSRF screen at funnel ingress and the adapter
  manifest host check at the result boundary. Adding an adapter means adding its manifest.
- **Only allowlisted candidate fields reach the LLM.** `product_probe` is the canary for that;
  do not widen the allowlist.
- **`internal/orders` makes no outbound calls and stores parsed fields only,** never raw MIME or
  message bodies in logs. A watch check ends in a notification and nothing else: there is no
  purchase path (an architecture test enforces it).
- **Stateful features use Postgres migrations** in `internal/postgres/migrations` (embedded, goose,
  applied at start). No embedded stores.
- **Versions come from release-please** (Conventional Commits, squash-merge). Never tag, or edit
  the version, `CHANGELOG.md` or `.release-please-manifest.json` by hand.
- **This repository is public.** No hostnames, operator paths, account names, secrets or
  personal data in any tracked file, tests and fixtures included.
