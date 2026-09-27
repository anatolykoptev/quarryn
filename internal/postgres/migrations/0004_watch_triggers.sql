-- +goose Up
-- Issues #93-96: watches get three trigger shapes instead of one —
-- absolute target price, percent-drop from a captured baseline, and
-- restock (unbuyable → buyable availability transition) — plus an
-- optional free-form condition_text evaluated by the match service on
-- every firing observation.
ALTER TABLE watches
    ALTER COLUMN target_price_minor DROP NOT NULL,
    ADD COLUMN notify_on       text NOT NULL DEFAULT 'price'
        CHECK (notify_on IN ('price', 'restock', 'any')),
    ADD COLUMN target_pct      int CHECK (target_pct IS NULL OR target_pct BETWEEN 1 AND 99),
    -- baseline_price_minor anchors target_pct; the first ok observation
    -- after creation sets it (NULL until then).
    ADD COLUMN baseline_price_minor bigint,
    ADD COLUMN condition_text  text NOT NULL DEFAULT '',
    -- Every watch needs at least one trigger: an absolute or pct target,
    -- or a mode that includes the restock transition.
    ADD CONSTRAINT watches_trigger_chk CHECK (
        target_price_minor IS NOT NULL
        OR target_pct IS NOT NULL
        OR notify_on IN ('restock', 'any'));

-- +goose Down
ALTER TABLE watches
    DROP CONSTRAINT watches_trigger_chk,
    DROP COLUMN condition_text,
    DROP COLUMN baseline_price_minor,
    DROP COLUMN target_pct,
    DROP COLUMN notify_on;
DELETE FROM watches WHERE target_price_minor IS NULL;
ALTER TABLE watches ALTER COLUMN target_price_minor SET NOT NULL;
