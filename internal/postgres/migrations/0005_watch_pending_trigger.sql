-- +goose Up
-- Review follow-ups on #93-96:
-- * pending_trigger remembers WHICH trigger fired while a notification
--   awaits delivery — a retry recomputes against the settled state and
--   would lose the restock/price label (Devin Review on PR #101).
-- * restock modes are offer-only: a query watch re-picks the cheapest
--   offer each check, so cross-listing availability flips would report
--   false restocks. Enforced at both the API and the row shape.
ALTER TABLE watches
    ADD COLUMN pending_trigger text NOT NULL DEFAULT '',
    ADD CONSTRAINT watches_restock_offer_chk
        CHECK (kind = 'offer' OR notify_on = 'price');

-- +goose Down
ALTER TABLE watches
    DROP CONSTRAINT watches_restock_offer_chk,
    DROP COLUMN pending_trigger;
