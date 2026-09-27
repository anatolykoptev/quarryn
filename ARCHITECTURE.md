# Architecture

Distilled current-state doc (the ADR series lives in the design record).
Package map:

```
cmd/server        wiring: config → pg → adapters → mux (REST+MCP) → watch checker
internal/
  sources         adapters (ebay, etsy, slickdeals, shopify) + funnel merge
                  + adapter manifests (ID/AllowedHosts/UserSession)
                  + offerid codec (stable per-adapter identity)
  extract         SERP-field normalize, schema.org detail parse,
                  fenced LLM fallback, condition enum
  match           deterministic prefilter + one packed jeff Ask/candidate
  rank            funnel/deal/jeff score fusion
  watch           watches: observer (offer re-fetch / query re-search),
                  checker (tick, 1% bucket, at-least-once ledger),
                  notify (dozor webhook), Postgres store
  orders          MIME parse (stdlib, multipart, HTML→text), retailer
                  rules + generic fallback, tracking regex, merge-on-ingest,
                  event ledger, Postgres store
  postgres        pgxpool + goose embedded migrations (0001 feedback,
                  0002 watches, 0003 orders)
  api             MCP tool surface + REST twins + deps wiring
  feedback        outcome sink: PG primary, JSONL fallback
  probe           live acceptance probes (jeff/wowa reachability,
                  injection canary)
  config          env → Config (single source of truth for env names)
```

## Request path (search)

```
product_search
  → sources.Collect: fan-out adapters, funnel merge,
    manifest host-gate at the result boundary (URL outside
    AllowedHosts → dropped) + SSRF screen at funnel ingress
  → offerid assigned once at candidateFromResult
  → extract: SERP fields → detail page (schema.org) → fenced LLM
    fallback; CandidateState carries only the allowlist fields
  → match: deterministic prefilter (price/condition/availability/
    not_keyword — fail-closed) then packed jeff Ask for the survivors
  → rank: fused score; degraded responses flag + reason
```

## Stateful services

- **Watches**: `watch.Store` (PG) + `Checker` (tick loop in main) +
  `Observer` (offer path via `MatchURL`, query path via `SearchDetailed`,
  hard currency match) + `notify` (dozor). Check ends in a notification
  and nothing else — no purchase path (enforced by architecture test).
- **Orders**: delivery-agnostic `Ingest(store, rawEML)` — two transports
  (IMAP poller, CF worker) call the same function. Merge on
  `(retailer_domain, order_no)`; `order_events` append-only. The package
  makes zero outbound calls.

## Egress rules

- All third-party page traffic goes through go-wowa (`WOWA_URL`), except
  api-class adapters (ebay/etsy) hitting first-party JSON over direct
  HTTPS.
- `dozor` resolves to the backend-net gateway via a compose alias —
  watches reach the alertmanager webhook at `172.18.0.1:8765`.
- Orders package: no egress. Worker/poller ingress: bearer-authed raw
  MIME only.

## Invariants worth knowing

- Money is minor units (`money.ToMinor`) end-to-end — no floats stored.
- `INTERNAL_SERVICE_SECRET` empty → all routes 401 (fail-closed).
- Unknown retailer → honest absence (no guessed return windows); unparseable
  mail → `unparsed` order row, not a silent drop.
- `offer_id` fallbacks are honest: `url|<hash>` + `native_id=false`, never
  masquerade as listing pins.
