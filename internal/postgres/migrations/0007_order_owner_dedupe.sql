-- +goose Up
-- Tenant isolation (Devin Review #106): order dedupe is per-owner — the
-- same (retailer_domain, order_no) under two owners are two orders, so
-- one tenant's ingest can never merge into another tenant's row.
DROP INDEX orders_dedupe_idx;
CREATE UNIQUE INDEX orders_dedupe_idx ON orders (retailer_domain, order_no, owner)
    WHERE order_no <> '';

-- +goose Down
DROP INDEX orders_dedupe_idx;
CREATE UNIQUE INDEX orders_dedupe_idx ON orders (retailer_domain, order_no)
    WHERE order_no <> '';
