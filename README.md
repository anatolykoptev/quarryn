# quarryn

[![preflight](https://github.com/anatolykoptev/quarryn/actions/workflows/preflight.yml/badge.svg)](https://github.com/anatolykoptev/quarryn/actions/workflows/preflight.yml)
[![release](https://img.shields.io/github/v/release/anatolykoptev/quarryn)](https://github.com/anatolykoptev/quarryn/releases)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**Agent-native product search and post-purchase tracking.** A query fans
out to marketplace adapters, passes deterministic constraint gates and an
LLM-judged match, and comes back as ranked offers with stable IDs — plus
persistent price watches and order tracking from confirmation emails.

MCP server + REST bridge; bearer-authed on every route except `GET /healthz`.

## Example

```sh
curl -s -X POST http://localhost:8922/api/tools/product_search \
  -H "Authorization: Bearer $INTERNAL_SERVICE_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "sony wh-1000xm5",
    "criteria": ["price_max:300", "condition:new", "availability:in_stock",
                 "good for travel"],
    "max_results": 5
  }'
```

```json
{
  "request_id": "9acef68f-…",
  "results": [{
    "offer_id": "slickdeals|20060145",
    "name": "Sony WH-1000XM5 Wireless Headphones",
    "price_minor": 27800, "currency": "USD",
    "condition": "new", "availability": "in_stock",
    "source": "slickdeals.net", "adapter": "slickdeals",
    "score": 0.87, "confidence": "high", "passed": true,
    "matched_criteria": [{"criterion": "good for travel", "passed": true}],
    "url": "https://slickdeals.net/f/20060145-…"
  }],
  "sources": [{"adapter": "ebay", "status": "ok", "candidates": 12}],
  "degraded": false
}
```

Hard criteria (`price_max`, `condition`, `availability`, `not_keyword`,
`brand`, `currency`) are enforced in code — fail-closed, never a prompt
suggestion. Subjective criteria ("good for travel") are judged per
candidate by the LLM, one packed verdict each, bounded by candidate and
concurrency caps. Every offer gets a durable `offer_id` — the anchor
watches and re-probes pin to.

## Features

- **Multi-source adapters** — eBay, Etsy, Slickdeals, Shopify storefronts;
  machine-readable/API tier only, no login-walled scraping.
- **Deterministic gates** — price/condition/availability constraints in
  code; SSRF screen at funnel ingress; per-adapter host manifests.
- **LLM match, honestly degraded** — subjective fit judged per candidate;
  on outage `degraded:true` + reason, and the rank share drops to
  deterministic features instead of silently guessing.
- **Price watches** — Postgres-backed, re-fetch offers or re-run queries
  on a cadence; alert on price ≤ target with 1% re-notify bucket,
  at-least-once ledger, `expires_at`, honest `unverifiable` state.
- **Order tracking** — forward order-confirmation emails (Cloudflare Email
  Worker push or IMAP label poll) → durable order graph: merge on
  `(retailer_domain, order_no)`, tracking deep links, return-by dates,
  append-only events.
- **Prompt-injection canary** — `product_probe` proves scraped page
  content can't reach the LLM verbatim (allowlisted fields only).
- **Bounded by default** — page/LLM/verdict budgets per search, per-domain
  pacing, body caps; nothing unbounded leaves the process.

## Tools

| Tool | What |
|---|---|
| `product_search` | Full pipeline: adapters → extract → match → rank |
| `product_match` | Judge one caller-supplied product URL |
| `product_watch` | `add\|list\|cancel\|check_now` price watches |
| `product_order` | `ingest_eml\|list\|get\|mark` order graph |
| `product_feedback` | Outcome record `{request_id, picked_url, verdict}` |
| `product_probe` | Live acceptance probes |

REST twins: `POST /api/tools/<tool>` with the same JSON arguments, plus
`POST /api/v1/feedback` and `POST /api/v1/orders/ingest` (raw RFC822,
2 MiB cap). MCP clients talk to it like any MCP server.

## Quickstart

Vendored deps — builds offline:

```sh
make build                                   # → bin/quarryn
INTERNAL_SERVICE_SECRET=$(openssl rand -hex 32) ./bin/quarryn
```

Everything else is optional and degrades loudly: `DATABASE_URL` enables
watches/orders/feedback persistence; eBay/Etsy adapters run standalone on
their public APIs (`EBAY_*`, `ETSY_*` keys); fetch-class adapters and LLM
matching expect a go-wowa-compatible scrape endpoint (`WOWA_URL`) and a
jeff-compatible decision endpoint (`JEFF_URL`) — unset, they go dark and
say so. Full env table: `docs/OPERATIONS.md`.

---

Apache-2.0 · Contributing: `make preflight` gates every merge — see
`CONTRIBUTING.md` · Architecture and API details under `docs/`.
