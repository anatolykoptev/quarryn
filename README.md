# quarryn

[![preflight](https://github.com/anatolykoptev/quarryn/actions/workflows/preflight.yml/badge.svg)](https://github.com/anatolykoptev/quarryn/actions/workflows/preflight.yml)
[![release](https://img.shields.io/github/v/release/anatolykoptev/quarryn)](https://github.com/anatolykoptev/quarryn/releases)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Agent-native product search and post-purchase tracking. A query goes
through marketplace adapters, deterministic constraint gates and an
LLM-judged match, and comes back as ranked offers with stable IDs.
On top of search sit two stateful pillars: price watches that alert on
target hits, and order tracking that parses confirmation emails into a
durable order graph.

MCP server + REST bridge, bearer-authed on every route except
`GET /healthz`.

## What it does

- **Search & match** — fan-out over marketplace adapters (eBay, Etsy,
  Slickdeals, Shopify storefronts), normalization to a common shape
  (minor-unit money, condition enum, availability), deterministic
  fail-closed gates (`price_max`, `condition:new`, `not_keyword`…),
  then one packed LLM verdict per surviving candidate and fused ranking.
  Subjective criteria go to the model; hard criteria are code.
- **Stable offer identity** — every offer carries a durable `offer_id`
  (`slickdeals|20060145`, honest `url|<hash>` fallback) that watches and
  re-probes anchor to.
- **Price watches** — re-fetch a pinned offer or re-run a query on a
  cadence; notify on price ≤ target with a 1% re-notify bucket,
  at-least-once ledger and `expires_at`. Offers that stop being
  extractable report `unverifiable` instead of silently going stale.
- **Order tracking** — raw RFC822 order-confirmation emails → durable
  order graph: per-retailer parse rules + generic fallback, merge on
  `(retailer_domain, order_no)`, carrier tracking deep links, return-by
  computation, append-only event ledger. Two transports: a Cloudflare
  Email Worker push or an IMAP label poller.
- **Honest degradation** — LLM or egress outages surface as
  `degraded:true` + a reason, never as silently deterministic results
  pretending to be judged.

## Tool surface

| Surface | What |
|---|---|
| `product_search` (MCP + `POST /api/tools/product_search`) | Full pipeline; returns `request_id`, ranked `results`, `sources`, `degraded` |
| `product_match` (MCP + `POST /api/tools/product_match`) | Judge one caller-supplied product URL through the same extract+match path |
| `product_feedback` (MCP + `POST /api/v1/feedback`) | Append the outcome record `{request_id, picked_url, verdict}` to Postgres (JSONL fallback) |
| `product_watch` (MCP + `POST /api/tools/product_watch`) | Price watches: `add\|list\|cancel\|check_now` on offers or queries; alerts via Alertmanager webhook |
| `product_order` (MCP + `POST /api/tools/product_order`) | Order tracking: `ingest_eml\|list\|get\|mark`; orders graph on Postgres |
| `POST /api/v1/orders/ingest` | Raw RFC822 order-confirmation ingest (2 MiB cap) — the mail-forwarder/Cloudflare-Worker shape |
| `product_probe` (MCP + `POST /api/tools/product_probe`) | Acceptance probes: LLM/egress reachability + a prompt-injection canary |
| `GET /healthz` | Open health probe (no auth) |
| `GET /metrics` on `PROM_PORT` | Prometheus scrape |

## Quickstart

The repo vendors all dependencies — the binary builds offline:

```sh
make build        # → bin/quarryn
```

Minimal run:

```sh
INTERNAL_SERVICE_SECRET=$(openssl rand -hex 32) ./bin/quarryn
```

`INTERNAL_SERVICE_SECRET` is required — empty fails closed (every route
401s). Everything else is optional and degrades honestly. See
`docs/OPERATIONS.md` for the full configuration table.

## Running it standalone

Heads-up on dependencies: the pipeline was built alongside companion
services. What works without them:

- **eBay / Etsy adapters** — first-party public APIs over direct HTTPS;
  need `EBAY_CLIENT_ID`/`EBAY_CLIENT_SECRET` and `ETSY_API_KEY`/
  `ETSY_SHARED_SECRET`. Fully standalone.
- **Slickdeals / Shopify adapters** — page egress goes through a
  go-wowa-compatible scrape endpoint (`WOWA_URL`); without one these
  adapters stay dark (registered but skipped — visible in the startup
  `adapters` log line).
- **LLM matching** — a jeff-compatible decision endpoint (`JEFF_URL`);
  empty → deterministic ranking only, `degraded` flagged on responses.
- **Postgres** (`DATABASE_URL`) — watches, orders and the feedback sink;
  empty → those tools report unavailable, search still works.
- **Watch alerts** — `WATCH_NOTIFY_URL` expects any Alertmanager v4
  webhook endpoint.

In other words: the deterministic spine (adapters → gates → ranking,
probes, MCP/REST surface) runs anywhere; subjective matching and
fetch-class scraping need their companion endpoints.

## Docs

- `docs/API.md` — tool arguments, REST routes, ingest transports
- `docs/OPERATIONS.md` — full env table, deployment shape, failure semantics
- `ARCHITECTURE.md` — package map, request path, invariants
- `SECURITY.md` — auth model, SSRF gates, reporting
- `CONTRIBUTING.md` — gate (`make preflight`), conventions
- `PRODUCT.md` — why it exists, positioning

## License

Apache-2.0 — see `LICENSE`.
