# Operations

Reference deployment shape: a Docker container behind a TLS-terminating
reverse proxy (Caddy/nginx), a Postgres instance on a private network, and
optional Redis. All inbound routes except `GET /healthz` require the
bearer secret.

## Services

| Unit | Port | Notes |
|---|---|---|
| `quarryn` (container) | `127.0.0.1:8922` | MCP + REST |
| `quarryn` metrics | `9922` | `/metrics`, Prometheus scrape |
| Postgres | private net only | `postgres:18-alpine`; goose migrations apply at service start |
| `quarryn-orders-imap.timer` (systemd --user) | — | Optional IMAP label poller for order mail |
| Cloudflare Worker `quarryn-order-ingest` | — | Optional push transport: mailbox → raw MIME POST |

## Data stores

- **Postgres**: `feedback`, `watches` + `watch_observations`,
  `orders` + `order_events`. Migrations are embedded in
  `internal/postgres/migrations/` and run at service start.
- **Redis** (optional, db index `QUARRYN_REDIS_DB`=7): L2 extraction
  cache; empty `REDIS_URL` = L1 only.
- **File fallback**: `FEEDBACK_FILE` JSONL absorbs feedback rows when PG
  writes fail — calibration data is never silently dropped.

## Configuration

`internal/config/config.go` is the env source of truth. Full table:

| Env | Default | Meaning |
|---|---|---|
| `PORT` | `8922` | MCP/REST listener |
| `PROM_PORT` | `9922` | `/metrics` listener (PORT+1000 convention) |
| `WOWA_URL` | `http://127.0.0.1:8906` | go-wowa-compatible scrape endpoint — ALL third-party page egress goes through it |
| `JEFF_URL` | — | jeff-compatible decision service. Empty → degrade mode (deterministic ranking only, flagged loudly) |
| `JEFF_TOKEN` | — | Bearer key for the decision service (any entry in its `JEFF_API_KEYS`) |
| `INTERNAL_SERVICE_SECRET` | — | Bearer secret on all routes but `/healthz`. Empty fails closed (everything 401s) |
| `REDIS_URL` | — | Optional L2 extraction cache; empty = L1 only |
| `QUARRYN_REDIS_DB` | `7` | Dedicated Redis DB index for the L2 cache |
| `JEFF_MATCH_MIN` | `0.55` | Per-criterion pass threshold |
| `MAX_JEFF_CANDIDATES` | `20` | Cap on verdict calls per search (overflow → `over_candidate_cap`, not a degrade) |
| `JEFF_CONCURRENCY` | `3` | In-flight verdict-call bound |
| `JEFF_TIMEOUT` | `10s` | Per-call deadline |
| `EXTRACT_LLM_TOP_N` | `10` | Fenced `/extract` LLM fallback only for top-N funnel candidates |
| `EXTRACT_MAX_DETAIL_FETCHES` | `15` | Detail-page fetches per search |
| `EXTRACT_CONCURRENCY` | `4` | Parallel candidate enrichment |
| `EXTRACT_FETCH_TIMEOUT_SECS` | `25` | Per-fetch wire timeout handed to the egress layer |
| `EXTRACT_CANDIDATE_TIMEOUT` | `45s` | Whole per-candidate extraction chain bound |
| `EXTRACT_CACHE_ITEMS` | `2000` | L1 extraction-cache size |
| `EXTRACT_LLM_DAILY_MAX` | `50` | Process-local `/extract` cap per UTC day (resets on restart) |
| `MAX_PAGES_PER_SEARCH` | `30` | Total fetch/render calls one search may place (SERP + detail share it) |
| `DOMAIN_MIN_INTERVAL_MS` | `2000` | Per-domain pacing floor; `0` disables pacing. Throttles back off 2s→4s→8s, max 3 retries, then the domain is skipped for that request |
| `DATABASE_URL` | — | Postgres DSN: feedback sink, watches, orders; empty = those features unavailable |
| `GROUPS_DATABASE_URL` | — | pgvector-capable Postgres DSN for the persistent product-group registry (issue #98 embedding tier); empty = ephemeral exact-only grouping |
| `EMBED_URL` | — | Fleet embed-server (OpenAI-compatible `/v1/embeddings`); `EMBED_TOKEN` bearer is auto-read by go-kit. Empty = embedding tier off |
| `EMBED_MODEL` | `multilingual-e5-large` | Embed model name served by the embed-server |
| `EMBED_DIM` | `1024` | Embedding dimension — must match the model; a mismatch fails schema setup and disables the registry |
| `GROUP_EMBED_THRESHOLD` | `0.90` | Cosine floor for a gated merge when both names carry model codes |
| `GROUP_EMBED_WEAK_THRESHOLD` | `0.94` | Cosine bar when a model code is missing on either side |
| `GROUP_TOPK` | `5` | Nearest-centroid candidates evaluated per result |
| `FEEDBACK_FILE` | `/var/lib/quarryn/feedback.jsonl` | JSONL fallback log when PG writes fail (records logged, never dropped) |
| `WATCH_TICK` | `15m` | Watch checker cadence |
| `WATCH_MAX_PER_TICK` | `10` | Watches processed per tick |
| `WATCH_NOTIFY_URL` | — | Webhook for watch alerts; empty = alerts fail loudly and retry |
| `WATCH_NOTIFY_FORMAT` | `alertmanager` | Payload shape: `alertmanager` (v4) or `json` — flat body `{event, trigger, watch_id, price_minor, availability, summary, …}` for ntfy/Gotify/custom sinks |
| `BOT_NOTIFY_URL` | — | Bot delivery endpoint — watches with `owner="tg:*"` POST their JSON alert here instead of `WATCH_NOTIFY_URL` |
| `BOT_NOTIFY_SECRET` | — | HMAC key for the bot endpoint — signs each POST (`X-Webhook-Timestamp` + `X-Webhook-Signature-V2`, hex HMAC-SHA256 over `<unix>.<body>`; the Hermes gateway scheme) |
| `WATCH_NOTIFY_SECRET` | — | Same HMAC signing for the generic `json` sink when it needs it |
| `WATCH_OWNER_MAX` | `25` | Active-watch cap per tenant owner (`0` = unlimited) |
| `TRUST_ALLOW_DOMAINS` / `TRUST_DENY_DOMAINS` | — | Domain trust overrides (CSV of base domains; deny beats allow) |
| `TOOL_TIMEOUT` | `90s` | Default per-tool deadline |
| `TOOL_TIMEOUT_SEARCH` | `3m` | `product_search` deadline (scrape + judge is slow); `product_watch` follows this tier |
| `TOOL_TIMEOUT_MATCH` | `1m` | `product_match` deadline |
| `RANK_FUNNEL_WEIGHT` | `0.3` | Fused-score share of funnel consensus |
| `RANK_DEAL_WEIGHT` | `0.2` | Fused-score share of deal signals |
| `RANK_JEFF_WEIGHT` | `0.5` | Fused-score share of LLM verdicts (auto-drops to 0 on degraded batches) |
| `EBAY_CLIENT_ID` / `EBAY_CLIENT_SECRET` | — | eBay Browse API creds; adapter dark without them |
| `ETSY_API_KEY` / `ETSY_SHARED_SECRET` | — | Etsy v3 API creds; adapter dark without them |
| `SHOPIFY_SHOPS` | — | Comma-separated shop domains for the products.json adapter |

### Budgets, caps and degraded mode

Three budget layers, all "rank what exists" — never fatal to the search:

- **Page budget** (`MAX_PAGES_PER_SEARCH`): every fetch/render —
  adapter SERP call or detail fetch — counts. On cap, detail fetches stop
  and results rank on SERP fields alone.
- **LLM budget** (`EXTRACT_LLM_DAILY_MAX`): process-local daily cap on
  `/extract` calls. Spent → candidates stay unenriched
  (`llm_budget_exhausted`), search continues.
- **Verdict budget** (`MAX_JEFF_CANDIDATES` + `JEFF_CONCURRENCY` +
  `JEFF_TIMEOUT`): bounds call count and queue wait.

`degraded:true` on a response means some candidates lost their verdicts
to a service-side failure (`jeff_saturated`/`jeff_unavailable`/
`jeff_timeout`/`jeff_http_error`/`jeff_no_answer`/`jeff_unconfigured`) —
`degrade_reason` carries the dominant cause and count; the verdict rank
share drops to 0 so degraded batches rank on deterministic features.
`over_candidate_cap` and `ctx_deadline` mark individual candidates but are
budget/caller data, not service failures — they never set `degraded`.

## Mail ingest

Two transports into `POST /api/v1/orders/ingest` (raw RFC822, bearer):

- **Push**: a Cloudflare Email Worker receives mail for a dedicated
  mailbox on a Cloudflare-routed domain and POSTs the raw MIME to the
  ingest endpoint over HTTPS. Expose only that path on the public vhost.
- **Pull**: an IMAP poller (`quarryn-orders-imap` systemd timer) reads a
  dedicated mailbox label every few minutes, POSTs unseen messages, and
  marks them `\Seen` only after a 2xx. Env: mailbox credentials +
  `INTERNAL_SERVICE_SECRET`. Logs: `journalctl --user -u quarryn-orders-imap`.

Duplicates merge on `(retailer_domain, order_no)` — both pipes can be
active without double-creating orders.

## Health and observability

- `GET /healthz` — liveness.
- `GET /metrics` (`PROM_PORT`) — `quarryn_*` series incl.
  `quarryn_probe_total{probe,result}`.
- `product_probe` tool — live acceptance probes (jeff/wowa reachable,
  injection canary).
- Watch alerts → `WATCH_NOTIFY_URL` webhook → your notifier (Telegram, etc).
- Startup log lines to look for: `goose: no migrations to run`,
  `watch checker started`, `feedback: primary sink is postgres`.

## Failure semantics

- PG down at start → feedback falls back to `FEEDBACK_FILE`; watches and
  orders unavailable (`503` on ingest), service stays up.
- Jeff down → `degraded:true` responses, deterministic ranking only.
- wowa down → fetch-class adapters fail their probes; api-class adapters
  (ebay/etsy) unaffected.
