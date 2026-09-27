# Operations

Deployment repo: `deploy/krolik-server` (`compose/search.yml`).

## Services

| Unit | Port | Notes |
|---|---|---|
| `go-product-search` (container) | `127.0.0.1:8922` | MCP + REST |
| `go-product-search` metrics | `9922` | `/metrics`, Prometheus scrape |
| `postgres-gps` (container) | backend net only | `postgres:18.4-alpine`, db `product_search`, volume mounted at `/var/lib/postgresql` (PG18 layout, `pg_upgrade --link`-safe) |
| `gps-orders-imap.timer` (systemd --user) | — | Gmail `Krolik/orders` label poll every 3 min |
| Cloudflare Worker `gps-order-ingest` | — | `orders@krolik.run` → raw MIME POST; source + deploy notes in `deploy/krolik-server/cloudflare/workers/` |

## Data stores

- **Postgres 18** (`postgres-gps`, `product_search` db): `feedback`,
  `watches` + `watch_observations`, `orders` + `order_events`.
  Migrations: goose, embedded in `internal/postgres/migrations/`,
  applied at service start. Current version: 3.
- **Redis** (optional, db index `PRODSEARCH_REDIS_DB`=7): L2 extraction
  cache; empty `REDIS_URL` = L1 only.
- **File fallback**: `FEEDBACK_FILE` JSONL absorbs feedback rows when PG
  writes fail — calibration data is never silently dropped.

## Configuration

Full env table lives in `internal/config/config.go` (source of truth).
Operational knobs:

| Env | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | — | `postgres://…` into `postgres-gps`; empty = watches/orders/feedback-PG disabled |
| `WATCH_TICK` | `15m` | Watch checker cadence |
| `WATCH_MAX_PER_TICK` | — | Bound per tick |
| `WATCH_NOTIFY_URL` | — | dozor webhook (`http://dozor:8765/webhook/alertmanager`; compose maps `dozor` to the backend-net gateway) |
| `TOOL_TIMEOUT` / `_SEARCH` / `_MATCH` | — | Per-tool timeouts; `product_watch` follows the search tier |
| `INTERNAL_SERVICE_SECRET` | — | Bearer on all routes; empty = everything 401s (fail-closed) |
| `TRUST_ALLOW_DOMAINS` / `TRUST_DENY_DOMAINS` | — | Domain trust overrides |
| `DOMAIN_MIN_INTERVAL_MS` | — | Per-domain pacing floor |

## Mail ingest

Two live pipes into `POST /api/v1/orders/ingest` (raw RFC822, bearer):

- **Push**: CF Email Worker → `https://orders.krolik.run` (Caddy vhost
  `conf.d/95-gps-orders.caddy` exposes only that path; TLS cert via
  nginx SNI → Caddy auto-HTTPS).
- **Pull**: `gps-orders-imap` timer. Units:
  `~/.config/systemd/user/gps-orders-imap.{service,timer}`, sources in
  `deploy/krolik-server/scripts/gps-orders-imap/`. Env from deploy `.env`
  (`SMTP_USER`/`SMTP_PASS` gmail app password, `INTERNAL_SERVICE_SECRET`).
  Logs: `journalctl --user -u gps-orders-imap`.

Duplicates merge on `(retailer_domain, order_no)` — both pipes can be
active without double-creating orders.

## Health and observability

- `GET /healthz` — liveness.
- `GET /metrics` (`PROM_PORT`) — `prodsearch_*` series incl.
  `prodsearch_probe_total{probe,result}`.
- `product_probe` tool — live acceptance probes (jeff/wowa reachable,
  injection canary).
- Watch alerts → dozor webhook → Telegram.
- Service logs: `docker logs go-product-search`; startup lines to look
  for: `goose: no migrations to run`, `watch checker started`,
  `feedback: primary sink is postgres`.

## Failure semantics

- PG down at start → feedback falls back to `FEEDBACK_FILE`; watches and
  orders unavailable (`503` on ingest), service stays up.
- Jeff down → `degraded:true` responses, deterministic ranking only.
- wowa down → fetch-class adapters fail their probes; api-class adapters
  (ebay/etsy) unaffected.
