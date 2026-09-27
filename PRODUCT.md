# go-product-search — Product Document

## Vision

An agent-native product discovery and post-purchase service. Not a scraper —
the durable layer between "what do I want to buy" and "did it arrive, can I
still return it". Built so that agent harnesses call it as a tool, humans get
alerts, and nobody rebuilds scraping infra per query.

## Problem

Product search today is split across deal aggregators (Slickdeals — manually
browsed, affiliate-driven), price trackers (Honey/Droplist — consumer silos,
no API for agents), and product-data APIs (Keepa, Rainforest — single-source,
per-token billing). None offer normalized multi-source results with
deterministic constraint enforcement behind one call.

Post-purchase is worse: order state lives inside retailer apps. Confirmation
emails — the one universal protocol every merchant already speaks — go
unstructured into an inbox.

Agent harnesses can do a one-off "find me X under $Y" browse. They cannot
watch a price for 30 days, dedupe notifications, merge a shipped-update into
the same order row, or guarantee a hard constraint — each run is stateless
and each guarantee is a prompt suggestion, not code.

## What it is

A self-hosted Go service on the krolik fleet, exposed over REST + MCP:

```
query → plan (deterministic criteria) → adapters (stealth scraping fleet)
      → normalize (money minor units, condition enum, availability)
      → gates (SSRF, adapter manifest hosts, price/condition/availability)
      → Jeff/Jev subjective match → rank → offer_id-stamped results
```

Four pillars:

- **Search & match** — multi-source product discovery with hard criteria
  (`condition:new`, price caps, availability) enforced in code; subjective
  fit ("suits a small apartment") goes to the LLM. MCP tools:
  `product_search`, `product_match`.
- **Stable offer identity** — every offer carries a durable `offer_id`
  (`slickdeals|20060145`, `url|<hash>` fallback, honest `native_id` flag) —
  the anchor everything else re-probes.
- **Price watches** — per-offer and per-query watches on Postgres 18:
  15-min checker, 1% re-notify bucket, at-least-once notification ledger,
  `unverifiable` honesty, `expires_at` on every watch. Alerts via
  dozor → Telegram. MCP: `product_watch`.
- **Order tracking** — order-confirmation emails → durable order graph:
  two ingest pipes live (Gmail `Krolik/orders` IMAP poller @3min, Cloudflare
  Email Worker `orders@krolik.run` push), per-retailer parse rules +
  generic fallback, merge on `(retailer_domain, order_no)`, carrier
  tracking numbers + deep links, return-by computation, append-only event
  ledger. REST `POST /api/v1/orders/ingest`, MCP `product_order`.

## Positioning

| Class | Example | What they do | What they lack |
|---|---|---|---|
| Agent harnesses | Operator, Manus | one-off browse-answer | state, schedule, determinism, dedup |
| Data APIs | Keepa, Rainforest | Amazon price/product data | multi-source, subjective match, watches |
| Deal trackers | Honey, Droplist | consumer price alerts | agent API, hard constraints, self-host |
| Deal sites | Slickdeals | human deal discovery | automation surface |

This service is a **tool for the harnesses**, not a competitor — MCP is the
interface precisely so agents consume it instead of re-deriving scraping
infrastructure from prompts.

## Moat

1. **Persistence** — watches, orders, ledgers: things that must outlive a
   request. Agent runs end; the watch doesn't.
2. **Deterministic gates** — constraints enforced in code, fail-closed.
3. **Fleet economics** — near-zero marginal cost: own stealth HTTP/browser
   infra (go-wowa), own LLM (Jeff/Jev), own Postgres. Per-query cost ≈ 0,
   unlike per-token SaaS competitors.
4. **Fleet reuse** — dozor alerts, Caddy edge, MCP auth, pg18 — no parallel
   stack.

## Monetization

Ranked by effort-to-revenue:

1. **Affiliate links at egress** — partner-tag injection on outbound offer
   clicks + watch-alert clicks (eBay PN, Skimlinks, Impact). One code point
   (PublicProduct egress). Revenue follows traffic. Constraints: program
   ToS, no double-dipping already-affiliated links (Slickdeals).
2. **API for agent builders (primary bet)** — product data + watches behind
   one MCP/REST call; tiered per-query/watch pricing; API keys + quota on
   existing Postgres; billing via fleet go-billing. Precedent: Keepa,
   Rainforest, SerpApi pricing tiers prove willingness to pay.
3. **Niche Telegram deal bot** — watch UX already wired to Telegram via
   dozor; cook-group/collectibles niches pay $30–100/mo for monitors. Cheap
   MVP on existing infra.
4. **B2B price intelligence** — competitor-SKU watches for sellers
   (Prisync/Competera pricing). Parked: requires coverage + SLA breadth.

Explicit non-goals: executing purchases (LAW: check path ends in
notification — purchase path is a different business: PCI, fraud,
chargebacks), selling order data (privacy), consumer app UI.

## Roadmap

Near: affiliate egress injection; API-key + quota layer; carrier status
polling behind the existing `Tracker` seam; return-window reminders.
Mid: pgvector embeddings for cross-source offer dedup; additional adapters;
public MCP endpoint for hosted consumers.

## Success signals

Watch→alert conversion (alerts that users act on), order ingest coverage
(parsed vs unparsed share), paid API seats, affiliate CTR on alerts.
