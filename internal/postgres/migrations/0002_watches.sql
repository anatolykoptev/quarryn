-- +goose Up
-- Issue #53: price watches. watches = config + mutable check state +
-- notify ledger; watch_observations = append-only per-check rows, which
-- doubles as the price history.
CREATE TABLE watches (
    id              bigserial PRIMARY KEY,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('offer', 'query')),
    -- identity: codec offer id when known; url is the re-fetch target.
    offer_id        text NOT NULL DEFAULT '',
    native_id       boolean NOT NULL DEFAULT false,
    url             text NOT NULL DEFAULT '',
    label           text NOT NULL DEFAULT '',
    query           text NOT NULL DEFAULT '',
    criteria        jsonb NOT NULL DEFAULT '[]',
    target_price_minor bigint NOT NULL CHECK (target_price_minor > 0),
    currency        text NOT NULL,
    interval_minutes int NOT NULL CHECK (interval_minutes >= 15),
    status          text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'expired', 'unverifiable', 'cancelled')),

    -- check state
    last_checked_at   timestamptz,
    next_check_after  timestamptz NOT NULL DEFAULT now(), -- notify retryAfter lands here
    last_price_minor  bigint,
    last_availability text NOT NULL DEFAULT '',
    consec_failures   int NOT NULL DEFAULT 0,

    -- at-least-once notify ledger: pending is set BEFORE the send; a crash
    -- or failure leaves it set and the next check retries the notify.
    notify_pending          boolean NOT NULL DEFAULT false,
    last_notify_attempt_at  timestamptz,
    notified_price_minor    bigint,  -- 1%-of-target dedupe anchor
    notified_at             timestamptz,
    notify_count            int NOT NULL DEFAULT 0
);

-- Due-scan hits the two guards only; ORDER BY next_check_after is cheap
-- at watch-fleet scale.
CREATE INDEX watches_due_idx ON watches (next_check_after) WHERE status = 'active';

CREATE TABLE watch_observations (
    id            bigserial PRIMARY KEY,
    watch_id      bigint NOT NULL REFERENCES watches(id) ON DELETE CASCADE,
    ts            timestamptz NOT NULL DEFAULT now(),
    price_minor   bigint,
    currency      text NOT NULL DEFAULT '',
    availability  text NOT NULL DEFAULT '',
    offer_url     text NOT NULL DEFAULT '', -- query kind: winning offer
    offer_id      text NOT NULL DEFAULT '',
    outcome       text NOT NULL,            -- ok | fetch_failed | extract_empty | no_offers
    detail        text NOT NULL DEFAULT ''
);
CREATE INDEX watch_observations_watch_idx ON watch_observations (watch_id, ts DESC);

-- +goose Down
DROP TABLE IF EXISTS watch_observations;
DROP TABLE IF EXISTS watches;
