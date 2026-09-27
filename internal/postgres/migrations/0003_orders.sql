-- +goose Up
-- Issue #57: order graph from order-confirmation emails. orders carries
-- one row per (retailer, order_no); order_events is the append-only
-- ingest/status ledger.
CREATE TABLE orders (
    id              bigserial PRIMARY KEY,
    created_at      timestamptz NOT NULL DEFAULT now(),
    retailer_domain text NOT NULL,
    order_no        text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'confirmed'
        CHECK (status IN ('confirmed','shipped','delivered','returned','cancelled')),
    placed_at       timestamptz,
    total_minor     bigint,
    currency        text NOT NULL DEFAULT '',
    label           text NOT NULL DEFAULT '',
    email_from      text NOT NULL DEFAULT '',
    subject         text NOT NULL DEFAULT '',
    return_by       timestamptz,
    tracking_no     text NOT NULL DEFAULT '',
    carrier         text NOT NULL DEFAULT '',
    track_url       text NOT NULL DEFAULT ''
);

-- Same retailer + same order number = the same order; re-forwarded mail
-- merges instead of duplicating. '' order_no rows (unparsed email) are
-- excluded via the partial index so junk mail never collides.
CREATE UNIQUE INDEX orders_dedupe_idx ON orders (retailer_domain, order_no)
    WHERE order_no <> '';
CREATE INDEX orders_status_idx ON orders (status);
CREATE INDEX orders_return_by_idx ON orders (return_by)
    WHERE return_by IS NOT NULL AND status NOT IN ('delivered','returned','cancelled');

CREATE TABLE order_events (
    id       bigserial PRIMARY KEY,
    order_id bigint NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ts       timestamptz NOT NULL DEFAULT now(),
    kind     text NOT NULL,
    detail   text NOT NULL DEFAULT ''
);
CREATE INDEX order_events_order_idx ON order_events (order_id, ts DESC);

-- +goose Down
DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS orders;
