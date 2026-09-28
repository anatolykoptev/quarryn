-- +goose Up
-- Issue #115: variant-pinned offer watches. The selector matches a
-- variant id or an option-title substring from the product's variant
-- matrix; the observer follows that configuration's price/availability
-- instead of the listing's min-price SKU.
ALTER TABLE watches ADD COLUMN variant_sel text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE watches DROP COLUMN variant_sel;
