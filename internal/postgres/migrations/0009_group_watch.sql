-- +goose Up
-- Issue #98: kind=group watches — "watch the product, not one listing":
-- a watch pinned to a durable product-group id observes the cheapest
-- live member offer each check. watch_observations.group_id carries the
-- same identity so the price history joins to the group registry —
-- including offer/query observations whose winning URL is a known
-- member (price-history-aware grouping).
ALTER TABLE watches
    DROP CONSTRAINT watches_kind_check,
    ADD COLUMN group_id bigint,
    ADD CONSTRAINT watches_kind_check CHECK (kind IN ('offer', 'query', 'group')),
    ADD CONSTRAINT watches_group_chk CHECK (kind <> 'group' OR group_id > 0);

ALTER TABLE watch_observations ADD COLUMN group_id bigint;

-- +goose Down
ALTER TABLE watch_observations DROP COLUMN group_id;
ALTER TABLE watches
    DROP CONSTRAINT watches_group_chk,
    DROP CONSTRAINT watches_kind_check;
-- Rollback needs no 'group' rows left — delete them before restoring
-- the original check.
DELETE FROM watches WHERE kind = 'group';
ALTER TABLE watches
    ADD CONSTRAINT watches_kind_check CHECK (kind IN ('offer', 'query')),
    DROP COLUMN group_id;
