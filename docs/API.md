# API Reference

All surfaces are bearer-authenticated with `Authorization: Bearer
$INTERNAL_SERVICE_SECRET` except `GET /healthz`. MCP tools are also exposed
over REST at `POST /api/tools/<tool_name>` with the same JSON arguments.

## MCP tools

### `product_search`

Full pipeline: adapters → extract → match → rank.

| Arg | Type | Notes |
|---|---|---|
| `query` | string | Product search query |
| `criteria` | string[] | Deterministic `key:value` constraints (`price_max:500`, `brand:sony`, `not_keyword:refurbished`, `availability:in_stock`, `condition:new`, `currency:usd`) and/or free-text subjective requirements judged per product |
| `max_results` | int | Default 10, max 50 |

Returns `request_id`, ranked `results` (with `offer_id`), `sources`,
`degraded`/`degrade_reason`, per-candidate `exclusion` reasons.

### `product_match`

Judge one caller-supplied product URL through the same extract+match path.

| Arg | Type | Notes |
|---|---|---|
| `product_url` | string | Listing URL to fetch and judge |
| `criteria` | string[] | Same vocabulary as `product_search` |

### `product_feedback`

| Arg | Type | Notes |
|---|---|---|
| `request_id` | string | From the search/match response; also stamped on `jeff_gate` log events |
| `picked_url` | string | Listing the user picked |
| `verdict` | string | Optional outcome (`bought`, `wrong_price`, `out_of_stock`, `bad_match`) |

Persisted to Postgres `feedback` (file fallback if PG is down).

### `product_watch`

Price watches on Postgres. `action`: `add | list | cancel | check_now`.

| Arg | Type | Notes |
|---|---|---|
| `kind` | `offer|query` | `offer` re-fetches one pinned URL; `query` re-runs search and takes the cheapest passed offer in `currency` |
| `url` / `offer_id` | string | `add kind=offer`: page to re-fetch; `offer_id` optional stable id |
| `query` / `criteria` | string / string[] | `add kind=query`: search text + criteria |
| `label` | string | Human name for notifications |
| `target_price` | float | Notify when price ≤ target (1% re-notify bucket) |
| `currency` | string | ISO 4217, required |
| `ttl_hours` | int | Watch lifetime, default 720 (30d), capped 90d |
| `interval_minutes` | int | Check cadence; floors 60 (offer) / 360 (query) |
| `watch_id` | int | `cancel` / `check_now` target |
| `include_inactive` | bool | `list`: include cancelled/expired |

Notifications go through an Alertmanager v4 webhook (`WATCH_NOTIFY_URL`). Offers that stop
being extractable become `unverifiable` after repeated failures and stop
consuming budget.

### `product_order`

Order tracking. `action`: `ingest_eml | list | get | mark`.

| Arg | Type | Notes |
|---|---|---|
| `eml` | string | Raw RFC822 message (`ingest_eml`) |
| `eml_base64` | bool | Decode `eml` from base64 |
| `order_id` | int | `get` / `mark` target |
| `status` | `delivered\|returned\|cancelled` | `mark` |
| `active_only` | bool | `list`: hide terminal orders |

Dedups/merges on `(retailer_domain, order_no)` — re-sent confirmations and
`shipped` updates merge into the same order. Unparseable mail becomes an
`unparsed` row, never a silent drop.

## REST routes

| Route | Notes |
|---|---|
| `GET /healthz` | Open health probe (no auth) |
| `POST /api/tools/<tool>` | REST twin of every MCP tool above |
| `POST /api/v1/feedback` | `{request_id, picked_url, verdict}` |
| `POST /api/v1/orders/ingest` | Raw RFC822 body (`Content-Type: message/rfc822`), 2 MiB cap. The shape mail forwarders and the Cloudflare Email Worker produce |
| `GET /metrics` | Prometheus scrape, on `PROM_PORT` only |

## Ingest transports (orders)

- **Push**: a Cloudflare Email Worker bound to a dedicated mailbox
  (`orders@your-domain`) POSTs raw MIME to
  `https://<host>/api/v1/orders/ingest` with the bearer secret.
- **Pull**: an IMAP poller (e.g. a systemd timer every 3 min) reads a
  dedicated mailbox label/folder and POSTs unseen messages to the same
  endpoint, marking them `\Seen` only after a 2xx — at-least-once.

Both end in `orders.Ingest` — same dedup, same ledger.
