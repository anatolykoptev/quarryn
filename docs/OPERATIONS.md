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

Full env table lives in `internal/config/config.go` (source of truth).
Operational knobs:

| Env | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | — | Postgres DSN; empty = watches/orders/feedback-PG disabled |
| `WATCH_TICK` | `15m` | Watch checker cadence |
| `WATCH_MAX_PER_TICK` | `10` | Watches processed per tick |
| `WATCH_NOTIFY_URL` | — | Alertmanager v4 webhook for watch alerts (e.g. `http://<alert-router>:8765/webhook/alertmanager`); empty = alerts fail loudly and retry |
| `TOOL_TIMEOUT` / `_SEARCH` / `_MATCH` | — | Per-tool timeouts; `product_watch` follows the search tier |
| `INTERNAL_SERVICE_SECRET` | — | Bearer on all routes; empty = everything 401s (fail-closed) |
| `TRUST_ALLOW_DOMAINS` / `TRUST_DENY_DOMAINS` | — | Domain trust overrides |
| `DOMAIN_MIN_INTERVAL_MS` | `2000` | Per-domain pacing floor |

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
