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
| `criteria` | string[] | Deterministic `key:value` constraints (`price_max:500`, `brand:sony`, `not_keyword:refurbished`, `availability:in_stock`, `condition:new`, `currency:usd`) and/or free-text requirements. Machine-checkable specs inside free text — sizes `64GB`/`48–64GB`/`32GB or 64GB` (GB↔TB normalized) and Apple-silicon tiers `M5 Pro`/`M5 Max` — are enforced deterministically against name/description/variant titles: a contradiction excludes (`spec_mismatch`), no disclosed spec stays for judging |
| `max_results` | int | Default 10, max 50 |

Returns `request_id`, ranked `results` (with `offer_id`), `sources`,
`degraded`/`degrade_reason`, per-candidate `exclusion` reasons.

Each result's `url` is the purchase page — the resolved merchant URL when
an outbound hop resolved (deal aggregators like slickdeals), else the
listing URL. `source_url` keeps the originating listing when they differ;
`buy_url` mirrors the resolved merchant URL for compatibility.

Optioned/configurator listings add `variants[]` — the purchasable
configuration matrix (option label, `variant_id`, decimal `price`,
`available`, deep-link `url` with the variant preselected). In-stock
first, capped at 40; absent on single-variant listings. Shopify pages get
the matrix from the deterministic `/products/<handle>.js` mirror; other
stores from schema.org `ProductGroup.hasVariant` or adapter metadata.

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

Price/restock watches on Postgres. `action`: `add | list | get | cancel | check_now`.

| Arg | Type | Notes |
|---|---|---|
| `kind` | `offer|query` | `offer` re-fetches one pinned URL; `query` re-runs search and takes the cheapest passed offer in `currency`. Every observation reads the page live — the 24h extraction cache is bypassed so alerts never fire on stale prices |
| `url` / `offer_id` | string | `add kind=offer`: page to re-fetch; `offer_id` optional stable id |
| `variant` | string | `add kind=offer` only: pin one configuration — variant id or option-title substring (`"64GB"`). The observer follows that variant's price/availability/URL; a selector matching nothing fails closed (`no_offers`), never watches the wrong SKU |
| `query` / `criteria` | string / string[] | `add kind=query`: search text + criteria |
| `label` | string | Human name for notifications |
| `notify_on` | `price\|restock\|any` | Default `price`. `restock` fires on unbuyable→buyable transitions (`out_of_stock\|discontinued` → any orderable state); `any` = either trigger |
| `target_price` | float | Notify when price ≤ target (1% re-notify bucket). Required for `price`/`any` unless `target_pct` is set |
| `target_pct` | int 1–99 | Notify when price drops ≥N% from the first observed price (baseline) |
| `condition` | string | Optional free-form gate evaluated per check by the match service (needs `JEFF_URL`); a fired trigger notifies only if the condition passes. Max 500 runes |
| `currency` | string | ISO 4217, required |
| `ttl_hours` | int | Watch lifetime, default 720 (30d), capped 90d |
| `interval_minutes` | int | Check cadence; floors 60 (offer) / 360 (query) |
| `watch_id` | int | `get` / `cancel` / `check_now` target |
| `include_inactive` | bool | `list`: include cancelled/expired |
| `history` / `history_limit` | bool / int | `list`: attach observation history per watch (newest first). `get` always includes history. Default 100 rows, max 500 |
| `owner` | string | Tenant scope (`tg:<chat_id>` for bot users). `add` stamps it; `list`/`get`/`cancel`/`check_now` only see own rows. Empty = unscoped fleet caller. Active watches per owner capped by `WATCH_OWNER_MAX` |

Notifications POST to `WATCH_NOTIFY_URL`; `WATCH_NOTIFY_FORMAT` selects the
payload — `alertmanager` (v4 webhook, `trigger` label `price`/`restock`) or
`json` (flat body: `event`, `trigger`, `watch_id`, `price_minor`,
`availability`, `summary`, …) for generic sinks like ntfy or Gotify.
Watches with `owner="tg:*"` route to `BOT_NOTIFY_URL` instead —
the bot endpoint that delivers to the right chat. The payload adds
`chat_id` (owner sans the `tg:` prefix) so the sink templates the
recipient directly; `BOT_NOTIFY_SECRET` enables HMAC-V2 signing
(`X-Webhook-Signature-V2` + `X-Webhook-Timestamp` over `<unix>.<body>`). Offers
that stop being extractable become `unverifiable` after repeated failures
and stop consuming budget. A `condition` whose evaluator is unreachable or
rejects the observation fails closed — the check records the reason and
no alert is sent.

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
